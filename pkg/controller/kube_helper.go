package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/reference"
)

// metadataType contains the metadata type of a K8s object.
type metadataType string

const (
	typeLabels      metadataType = "labels"
	typeAnnotations metadataType = "annotations"
)

// These labels are set when a Pod interaction occurs and not supposed to change after.
const (
	PodInteractionTimestampLabel = "box.com/podInitialInteractionTimestamp"
	PodInteractorLabel           = "box.com/podInteractorUsername"
	PodTTLDurationLabel          = "box.com/podTTLDuration"
)

// These annotations are set when requesting extended termination time to an interacted Pod.
const (
	PodExtendDurationAnnotate  = "box.com/podExtendedDuration"
	PodExtendRequesterAnnotate = "box.com/podExtensionRequester"
	PodTerminationTimeAnnotate = "box.com/podTerminationTime"
)

// initEventRecorder returns a record.EventRecorder to submit K8s events.
func initEventRecorder(kubeClient kubernetes.Interface) record.EventRecorder {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartRecordingToSink(&typedv1.EventSinkImpl{
		Interface: kubeClient.CoreV1().Events(""),
	})
	component := "kube-exec-controller"
	return eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: component})
}

// submitEvent posts a K8s event to the target Pod with the given message.
func submitEvent(pod *corev1.Pod, message string, recorder record.EventRecorder) error {
	ref, err := reference.GetReference(scheme.Scheme, pod)
	if err != nil {
		zap.L().Error("Failed to submit K8s event to the target Pod",
			zap.String("pod_name", pod.Name),
			zap.String("pod_namespace", pod.Namespace),
			zap.String("event_message", message),
			zap.Error(err),
		)
		return err
	}

	reason := "PodInteraction"
	recorder.Event(ref, corev1.EventTypeWarning, reason, message)

	return nil
}

// evictPodFunc returns a function to evict a Pod specified by its name and namespace
func evictPodFunc(name, namespace string, kubeClient kubernetes.Interface) func() {
	return func() {
		err := kubeClient.PolicyV1beta1().Evictions(namespace).Evict(context.TODO(), &policy.Eviction{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		})
		if err != nil {
			zap.L().Error("Error in evicting a Pod!",
				zap.String("pod_name", name),
				zap.String("namespace", namespace),
				zap.Error(err),
			)
			return
		}

		zap.L().Info("Successfully evicted an interacted Pod.",
			zap.String("name", name),
			zap.String("namespace", namespace),
		)
	}
}

// patch updates a K8s Pod with given metadata type and values passed from a map.
// It returns the patched Pod.
func patch(pod corev1.Pod, dataType metadataType, dataMap map[string]string, kubeClient kubernetes.Interface) (
	*corev1.Pod, error) {
	var patchStrs []string
	var isEmpty bool
	if dataType == typeLabels {
		isEmpty = len(pod.Labels) == 0
	} else {
		isEmpty = len(pod.Annotations) == 0
	}
	if isEmpty {
		// metadata type has to exist before patching specific key/val dataMap
		emptyPatch := getJSONPatchStr(dataType, "", "")
		patchStrs = append(patchStrs, emptyPatch)
	}

	for key, val := range dataMap {
		patchStr := getJSONPatchStr(dataType, key, val)
		patchStrs = append(patchStrs, patchStr)
	}

	patchData := []byte(fmt.Sprintf("[%s]", strings.Join(patchStrs, ",")))
	patchOpts := metav1.PatchOptions{FieldManager: "kube-exec-controller"}
	return kubeClient.CoreV1().Pods(pod.Namespace).Patch(context.TODO(), pod.Name, types.JSONPatchType, patchData, patchOpts)
}

// getJSONPatchStr returns a JSON patch string from the given metadata type, key and value.
// It returns an empty patch string of the metadata type if the given key is empty.
func getJSONPatchStr(dataType metadataType, key, val string) string {
	if key == "" {
		return fmt.Sprintf("{\"op\":\"add\",\"path\":\"/metadata/%s\",\"value\":{}}", dataType)
	}

	// replace invalid characters in key to satisfy JSON patch format
	key = strings.ReplaceAll(key, "~", "~0")
	key = strings.ReplaceAll(key, "/", "~1")

	// replace invalid characters in label's val to satisfy K8s requirement
	if dataType == typeLabels {
		val = strings.ReplaceAll(val, ":", "_")
	}

	return fmt.Sprintf("{\"op\":\"add\",\"path\":\"/metadata/%s/%s\",\"value\":\"%s\"}",
		dataType, key, val)
}

// getTerminationTime returns the termination time by parsing current related metadata from the target Pod.
func getTerminationTime(pod corev1.Pod) (time.Time, error) {
	interactedTime, err := parseUnixTime(pod.Labels[PodInteractionTimestampLabel])
	if err != nil {
		return time.Time{}, err
	}

	ttlDuration, err := time.ParseDuration(pod.Labels[PodTTLDurationLabel])
	if err != nil {
		return time.Time{}, err
	}

	extendDuration := time.Duration(0)
	extendDurationStr, present := pod.Annotations[PodExtendDurationAnnotate]
	if present {
		extendDuration, err = time.ParseDuration(extendDurationStr)
		if err != nil {
			return time.Time{}, err
		}
	}

	return interactedTime.Add(ttlDuration).Add(extendDuration), nil
}

// parseUnixTime parses the given Unix time string and returns a time.Time object.
func parseUnixTime(str string) (time.Time, error) {
	timeInt, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return time.Time{}, err
	}

	return time.Unix(timeInt, 0), nil
}
