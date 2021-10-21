package controller_test

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/box/kube-exec-controller/pkg/controller"
)

// TestCheckPodInteraction tests controller checking both previously and newly interacted pods
func TestCheckPodInteraction(t *testing.T) {
	setupZapLogging(t)

	namespace := "test-namespace"
	interactedTime := time.Now()
	ttlDuration := time.Duration(2) * time.Second

	// create a previously (e.g. controller is restarted) interacted pod by setting related labels to it
	previousInteractedPodName := "test-pod-previous"
	previousInteractedPod := getPodObject(namespace, previousInteractedPodName)
	previousInteractedPod.SetLabels(map[string]string{
		controller.PodInteractionTimestampLabel: strconv.FormatInt(interactedTime.Unix(), 10),
		controller.PodTTLDurationLabel:          ttlDuration.String(),
	})

	// create a newly interacted pod by mocking a new pod interaction
	newInteractedPodName := "test-pod-new"
	interactedUsername := "test-user"
	mockPodInteraction(namespace, newInteractedPodName, interactedUsername, interactedTime)
	newInteractedPod := getPodObject(namespace, newInteractedPodName)

	fakeClient := fake.NewSimpleClientset(previousInteractedPod, newInteractedPod)
	contr := controller.NewController(fakeClient, int(ttlDuration.Seconds()))
	contr.CheckPodInteraction()

	// get the above two pods from kube client (which should have been updated by the controller)
	previousInteractedPod, err := fakeClient.CoreV1().Pods(namespace).Get(context.TODO(), previousInteractedPod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	newInteractedPod, err = fakeClient.CoreV1().Pods(namespace).Get(context.TODO(), newInteractedPod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// verify annotations (both pods should have annotations updated)
	terminationTime := interactedTime.Add(ttlDuration).Truncate(time.Second)
	expectedAnnotations := map[string]string{
		controller.PodTerminationTimeAnnotate: terminationTime.String(),
	}
	checkDeepEquals(t, expectedAnnotations, previousInteractedPod.GetAnnotations())
	checkDeepEquals(t, expectedAnnotations, newInteractedPod.GetAnnotations())

	// verify labels (the newly interacted pod should have its labels updated)
	expectedLabels := map[string]string{
		controller.PodInteractionTimestampLabel: strconv.FormatInt(interactedTime.Unix(), 10),
		controller.PodTTLDurationLabel:          ttlDuration.String(),
		controller.PodInteractorLabel:           interactedUsername,
	}
	checkDeepEquals(t, expectedLabels, newInteractedPod.GetLabels())

	// verify both interacted pods are evicted by the controller (kube client should return errors)
	time.Sleep(ttlDuration)
	pods, err := fakeClient.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
	if err == nil {
		t.Fatal("expected an error accessting to evicted, but got", pods)
	}
}

// TestCheckPodInteraction tests controller checking an extension of interacted pod
func TestCheckPodExtension(t *testing.T) {
	setupZapLogging(t)

	namespace := "test-namespace"
	interactedTime := time.Now()
	ttlDuration := time.Duration(2) * time.Second

	// mock an interaction so that we can test the extension on this pod
	podName := "test-pod"
	mockPodInteraction(namespace, podName, "", interactedTime)

	podObj := getPodObject(namespace, podName)
	// UID is used for updating termination timer by the controller
	podObj.SetUID(types.UID(podName))
	fakeClient := fake.NewSimpleClientset(podObj)
	contr := controller.NewController(fakeClient, int(ttlDuration.Seconds()))
	contr.CheckPodInteraction()

	// mock an extension request to the above pod
	interactedTestPod, err := fakeClient.CoreV1().Pods(namespace).Get(context.TODO(), podName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	extendDuration := time.Duration(2) * time.Hour
	interactedTestPod.SetAnnotations(map[string]string{
		controller.PodExtendDurationAnnotate: extendDuration.String(),
	})
	extendRequester := "test-user"
	extensionUpdate := controller.PodExtensionUpdate{
		Pod:      *interactedTestPod,
		Username: extendRequester,
	}
	controller.PodExtensionUpdateCh = make(chan controller.PodExtensionUpdate)
	go func() {
		defer close(controller.PodExtensionUpdateCh)

		controller.PodExtensionUpdateCh <- extensionUpdate
	}()
	contr.CheckPodExtensionUpdate()

	// verify the pod still exists after exceeding the original ttlDuration
	time.Sleep(ttlDuration)
	extendedTestPod, err := fakeClient.CoreV1().Pods(namespace).Get(context.TODO(), podName, metav1.GetOptions{})
	if err != nil {
		t.Fatal("expected the pod still exists, but failed to get it with err:", err)
	}

	// verify the pod's annotation contains extension info set by the controller
	terminationTime := interactedTime.Add(ttlDuration).Add(extendDuration).Truncate(time.Second)
	expectedAnnotaitons := map[string]string{
		controller.PodTerminationTimeAnnotate: terminationTime.String(),
		controller.PodExtendRequesterAnnotate: extendRequester,
	}
	checkDeepEquals(t, expectedAnnotaitons, extendedTestPod.GetAnnotations())
}

/*
  Helper functions used by the testings above.
*/

// setupZapLogging gives better visibility when running a test
func setupZapLogging(t *testing.T) {
	logger := zaptest.NewLogger(t)
	zap.ReplaceGlobals(logger)
}

// mockPodInteraction sends a new PodInteraction with the given namespace and pod name to PodInteractionCh
func mockPodInteraction(namespace, podName, interactor string, interactedTime time.Time) {
	podInteraction := controller.PodInteraction{
		PodNamespace: namespace,
		PodName:      podName,
		InitTime:     interactedTime,
		Username:     interactor,
	}

	controller.PodInteractionCh = make(chan controller.PodInteraction)
	go func() {
		defer close(controller.PodInteractionCh)

		controller.PodInteractionCh <- podInteraction
	}()
}

// getPodObject returns a new corev1.Pod object with tbe given namespace and pod name
func getPodObject(namespace, podName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
	}
}

func checkDeepEquals(t *testing.T, expected, actual interface{}) {
	if !reflect.DeepEqual(expected, actual) {
		t.Errorf("expected: %s, got: %s", expected, actual)
	}
}
