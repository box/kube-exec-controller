package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
)

// Channels for handling new Pod interactions and their extension updates.
var (
	PodInteractionCh     chan PodInteraction
	PodExtensionUpdateCh chan PodExtensionUpdate
)

// PodInteraction contains information about a Pod interaction occurrence.
type PodInteraction struct {
	PodName       string
	PodNamespace  string
	ContainerName string
	Username      string
	Commands      []string
	InitTime      time.Time
}

// MarshalLogObject makes PodInteraction struct loggable.
func (pi *PodInteraction) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddString("pod_name", pi.PodName)
	enc.AddString("pod_namespace", pi.PodNamespace)
	enc.AddString("container_name", pi.ContainerName)
	enc.AddString("username", pi.Username)
	enc.AddString("command_list", strings.Join(pi.Commands, ","))
	enc.AddTime("interacted_time", pi.InitTime)

	return nil
}

// PodExtensionUpdate contains an updated Pod object and a username who requests the update.
type PodExtensionUpdate struct {
	Pod      corev1.Pod
	Username string
}

// Controller ensures that interacted Pods are in the desired state.
type Controller struct {
	kubeClient           kubernetes.Interface
	recorder             record.EventRecorder
	podTTLDuration       time.Duration
	terminationTimersMap map[types.UID]*time.Timer
}

// NewController creates a new Controller with all required components set.
func NewController(kubeClient kubernetes.Interface, ttlSeconds int) Controller {
	return Controller{
		kubeClient:           kubeClient,
		recorder:             initEventRecorder(kubeClient),
		podTTLDuration:       time.Duration(ttlSeconds) * time.Second,
		terminationTimersMap: make(map[types.UID]*time.Timer),
	}
}

// CheckPodInteraction checks both previously existed Pod interactions at startup
// and all new interactions received from the channel with exponential backoff.
func (c *Controller) CheckPodInteraction() {
	ebo := backoff.NewExponentialBackOff()
	retryNotifier := func(err error, t time.Duration) {
		zap.L().Warn(
			fmt.Sprintf("Failed to handle a Pod interaction, will retry in %s", t.String()),
			zap.Error(err),
		)
	}

	// check previous Pod interactions (exist before controller restarts)
	if err := backoff.RetryNotify(c.handlePreviousInteraction, ebo, retryNotifier); err != nil {
		zap.L().Error("Error in retrying to check previous Pod interactions, giving up!", zap.Error(err))
	}
	ebo.Reset()

	// check new Pod interactions received from the channel
	for newInteraction := range PodInteractionCh {
		retryOperation := func() error { return c.handleNewInteraction(newInteraction) }
		if err := backoff.RetryNotify(retryOperation, ebo, retryNotifier); err != nil {
			zap.L().Error("Error in retrying to check a new Pod interaction, giving up!",
				zap.Object("pod_interaction", &newInteraction),
				zap.Error(err),
			)
		}
		ebo.Reset()
	}
}

// CheckPodExtensionUpdate checks Pod extension update received from the channel.
func (c *Controller) CheckPodExtensionUpdate() {
	ebo := backoff.NewExponentialBackOff()
	retryNotifier := func(err error, t time.Duration) {
		zap.L().Warn(
			fmt.Sprintf("Failed to handle a Pod extension update, will retry in %s", t.String()),
			zap.Error(err),
		)
	}

	for podUpdate := range PodExtensionUpdateCh {
		retryOperation := func() error { return c.handlePodExtensionUpdate(podUpdate) }
		if err := backoff.RetryNotify(retryOperation, ebo, retryNotifier); err != nil {
			zap.L().Error("Error in retrying to check a pod extension update, giving up!",
				zap.String("pod_name", podUpdate.Pod.Name),
				zap.String("pod_namespace", podUpdate.Pod.Namespace),
				zap.String("requester", podUpdate.Username),
				zap.Error(err),
			)
		}
		ebo.Reset()
	}
}

// handlePodExtensionUpdate resets termination time of the Pod and annotates username who requested the extension.
// It also submits a K8s event with all updated info to the target Pod.
func (c *Controller) handlePodExtensionUpdate(pd PodExtensionUpdate) error {
	// skip if no termination timer exists for the target Pod (could be expired or stopped)
	pod := pd.Pod
	if _, present := c.terminationTimersMap[pod.UID]; !present {
		zap.L().Warn("Failed to get the termination timer of an extension updated Pod, ignoring",
			zap.String("pod_name", pod.Name),
			zap.String("pod_namespace", pod.Namespace),
		)
		return nil
	}

	// reset the timer based on current termination metadata attached in the target Pod
	if err := c.setTermination(pod); err != nil {
		return err
	}

	// annotate extension requester to the target Pod
	annotationPatchMap := map[string]string{
		PodExtendRequesterAnnotate: pd.Username,
	}
	patchedPod, err := patch(pod, typeAnnotations, annotationPatchMap, c.kubeClient)
	if err != nil {
		return err
	}

	// submit a K8s event to the target Pod with the updated info
	newExtension := patchedPod.Annotations[PodExtendDurationAnnotate]
	newTerminationTime := patchedPod.Annotations[PodTerminationTimeAnnotate]
	message := fmt.Sprintf(
		"Pod eviction time has been extended by '%s', as requested from user '%s'. New eviction time: %s",
		newExtension, pd.Username, newTerminationTime)
	if err := submitEvent(patchedPod, message, c.recorder); err != nil {
		return err
	}

	zap.L().Info("Updated termination time of an interacted Pod with a new extension",
		zap.String("pod_name", pod.Name),
		zap.String("pod_namespace", pod.Namespace),
		zap.String("requester_username", pd.Username),
		zap.String("new_extension", newExtension),
		zap.String("new_termination_time", newTerminationTime),
	)

	return nil
}

// handlePreviousInteraction lists all running Pods that were previously interacted
// and sets termination to them based on their current metadata.
func (c *Controller) handlePreviousInteraction() error {
	options := metav1.ListOptions{LabelSelector: PodInteractionTimestampLabel}
	podList, err := c.kubeClient.CoreV1().Pods(corev1.NamespaceAll).List(context.TODO(), options)
	if err != nil {
		return err
	}

	for _, pod := range podList.Items {
		if err := c.setTermination(pod); err != nil {
			zap.L().Error("Error in setting termination timer to a previously interacted Pod, skipping.",
				zap.String("pod_name", pod.Name),
				zap.String("namespace", pod.Namespace),
				zap.Error(err),
			)
		}
	}

	return nil
}

// handleNewInteraction updates the target Pod and creates a timer to evict it later.
// It skips if the target Pod already has an interacted timestamp label set.
func (c *Controller) handleNewInteraction(pi PodInteraction) error {
	// locate the Pod in cluster from the given PodInteraction
	pod, err := c.kubeClient.CoreV1().Pods(pi.PodNamespace).Get(context.TODO(), pi.PodName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// ignore the Pod with an existing termination label (has been checked already)
	if val, present := pod.Labels[PodInteractionTimestampLabel]; present {
		zap.L().Debug("Pod has already been labeled with the interaction info, ignored.",
			zap.String("pod_name", pi.PodName),
			zap.String("pod_namespace", pi.PodNamespace),
			zap.String("pod_interaction_timestamp", val),
		)
		return nil
	}

	// submit a K8s event to the target Pod
	message := fmt.Sprintf(
		"Pod was interacted with 'kubectl exec/attach' command by a user '%s' initially at time %s",
		pi.Username,
		pi.InitTime.String(),
	)
	if err := submitEvent(pod, message, c.recorder); err != nil {
		return err
	}

	// set interaction related metadata to the target Pod
	updatedPod, err := c.setInteractionLabels(*pod, pi)
	if err != nil {
		return err
	}

	// set termination timer based on the above metadata
	if err := c.setTermination(*updatedPod); err != nil {
		return err
	}

	zap.L().Info("A new Pod interaction is detected and handled.", zap.Object("pod_interaction", &pi))

	return nil
}

// setInteractionLabels patches interaction related info as labels to the target Pod.
func (c *Controller) setInteractionLabels(pod corev1.Pod, pi PodInteraction) (*corev1.Pod, error) {
	timestamp := strconv.FormatInt(pi.InitTime.Unix(), 10)
	labelsPatchMap := map[string]string{
		PodInteractionTimestampLabel: timestamp,
		PodInteractorLabel:           pi.Username,
		PodTTLDurationLabel:          c.podTTLDuration.String(),
	}
	return patch(pod, typeLabels, labelsPatchMap, c.kubeClient)
}

// setTermination patches termination time as annotation to the target Pod and sets a timer
// in controller to evict the Pod. It calculates the termination time from Pod's metadata.
func (c *Controller) setTermination(pod corev1.Pod) error {
	terminationTime, err := getTerminationTime(pod)
	if err != nil {
		return err
	}
	annotationPatchMap := map[string]string{
		PodTerminationTimeAnnotate: terminationTime.String(),
	}
	if _, err := patch(pod, typeAnnotations, annotationPatchMap, c.kubeClient); err != nil {
		return err
	}

	// create or reset a timer to evict the target Pod with currently remaining duration
	remainDuration := time.Until(terminationTime)
	if timer, present := c.terminationTimersMap[pod.UID]; present {
		if success := timer.Reset(remainDuration); !success {
			zap.L().Warn("Failed to reset termination timer in a Pod (either expired or stopped)",
				zap.String("pod_name", pod.Name),
				zap.String("pod_namespace", pod.Namespace),
			)
			return nil
		}
	} else {
		newTimer := time.AfterFunc(remainDuration, evictPodFunc(pod.Name, pod.Namespace, c.kubeClient))
		c.terminationTimersMap[pod.UID] = newTimer
	}

	// submit a K8s event to the Pod with its termination time
	message := fmt.Sprintf("Pod will be evicted at time %s (in about %s)",
		terminationTime.String(),
		remainDuration.Round(time.Second).String(),
	)
	return submitEvent(&pod, message, c.recorder)
}
