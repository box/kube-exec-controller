package plugin

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEmptyCommand(t *testing.T) {
	testCmd := getTestInstance().cmd
	err := testCmd.RunE(testCmd, []string{})
	checkErrMsg(t, err, cmdArgsLengthError)
}

func TestInvalidAction(t *testing.T) {
	testCmd := getTestInstance().cmd
	err := testCmd.RunE(testCmd, []string{"invalid"})
	checkErrMsg(t, err, cmdInvalidActionError)

	err = testCmd.RunE(testCmd, []string{"123"})
	checkErrMsg(t, err, cmdInvalidActionError)
}

func TestInvalidArguments(t *testing.T) {
	testCmd := getTestInstance().cmd

	// testing empty value set for "--duration"
	testCmd.Flags().Set("duration", "")
	err := testCmd.RunE(testCmd, []string{cmdExtendAction, "test-pod"})
	checkErrMsg(t, err, cmdInValidDurationError)

	// testing invalid value set for "--duration" (missing unit suffix)
	testCmd.Flags().Set("duration", "30")
	err = testCmd.RunE(testCmd, []string{cmdExtendAction, "test-pod"})
	checkErrMsg(t, err, cmdInValidDurationError)

	// testing invalid value set for "--duration" (incorrect unit format)
	testCmd.Flags().Set("duration", "30 minutes")
	err = testCmd.RunE(testCmd, []string{cmdExtendAction, "test-pod"})
	checkErrMsg(t, err, cmdInValidDurationError)
}

func TestGetSpecifiedPods(t *testing.T) {
	testNamespace := "test-ns"
	testPodName1, testPodName2 := "test-pod-1", "test-pod-2"
	testPod1 := getFakePod(testPodName1, testNamespace, nil, nil)
	testPod2 := getFakePod(testPodName2, testNamespace, nil, nil)
	fakeClient := fake.NewSimpleClientset(testPod1, testPod2)
	fakeOptions := CmdOptions{}
	fakeOptions.kubeClient = fakeClient

	// testing specific pod names
	fakeOptions.namespace = testNamespace
	fakeOptions.podNames = []string{testPodName2}
	resPods, err := fakeOptions.getSpecifiedPods()
	if err != nil {
		t.Fatal(err)
	}
	expect := fakeOptions.podNames[0]
	result := resPods[0].Name
	checkMatches(t, expect, result)

	// testing all pods under the current namespace (--all flag set)
	fakeOptions.specifiedAll = true
	resPods, err = fakeOptions.getSpecifiedPods()
	if err != nil {
		t.Fatal(err)
	}
	if len(resPods) != 2 {
		t.Fatalf("expecting two pods but got %v", len(resPods))
	}
	podExistMap := make(map[string]bool)
	for _, pod := range resPods {
		podExistMap[pod.Name] = true
	}
	if !podExistMap[testPodName1] || !podExistMap[testPodName2] {
		t.Fatalf("missing %s or %s from %v", testPodName1, testPodName2, podExistMap)
	}
}

func TestHandleActionGet(t *testing.T) {
	podNamespace := "test-namespace"

	// a pod with no interaction
	noInteractionPodName := "test-pod-1"
	noInteractionPod := getFakePod(noInteractionPodName, podNamespace, nil, nil)

	// an interacted pod with no extension
	noExtensionPodName := "test-pod-2"
	noExtensionPodLabels := map[string]string{
		podInteractorLabel:  "test-interactor-2",
		podTTLDurationLabel: "30s",
	}
	noExtensionPodAnnotations := map[string]string{
		podTerminationTimeAnnotate: time.Now().String(),
	}
	noExtensionPod := getFakePod(noExtensionPodName, podNamespace, noExtensionPodLabels, noExtensionPodAnnotations)

	// an interacted pod with extension
	extendedPodName := "test-pod-3"
	extendedPodLabels := map[string]string{
		podInteractorLabel:  "test-interactor-3",
		podTTLDurationLabel: "45m",
	}
	extendedPodAnnotations := map[string]string{
		podTerminationTimeAnnotate: time.Now().String(),
		podExtendDurationAnnotate:  "2h",
		podExtendRequesterAnnotate: "test-requester-3",
	}
	extendedPod := getFakePod(extendedPodName, podNamespace, extendedPodLabels, extendedPodAnnotations)

	fakeClient := fake.NewSimpleClientset(noInteractionPod, noExtensionPod, extendedPod)

	fakeOptions := CmdOptions{}
	fakeOptions.kubeClient = fakeClient
	testOut := getTestInstance().out
	fakeOptions.Out = testOut

	// testing a no interaction pod
	testOut.Reset()
	if err := fakeOptions.handleActionGet([]corev1.Pod{*noInteractionPod}); err != nil {
		t.Fatal(err)
	}
	checkStrContainsAll(t, []string{noInteractionPodName}, testOut.String())

	// testing a no extension pod
	testOut.Reset()
	if err := fakeOptions.handleActionGet([]corev1.Pod{*noExtensionPod}); err != nil {
		t.Fatal(err)
	}
	checkStrContainsAll(t, []string{noExtensionPodName}, testOut.String())
	checkStrContainsAll(t, getAllValues(noExtensionPodLabels), testOut.String())
	checkStrContainsAll(t, getAllValues(noExtensionPodAnnotations), testOut.String())

	// testing an extended pod
	testOut.Reset()
	if err := fakeOptions.handleActionGet([]corev1.Pod{*extendedPod}); err != nil {
		t.Fatal(err)
	}
	checkStrContainsAll(t, []string{extendedPodName}, testOut.String())
	checkStrContainsAll(t, getAllValues(extendedPodLabels), testOut.String())
	checkStrContainsAll(t, getAllValues(extendedPodAnnotations), testOut.String())
}

func TestHandleActionExtend(t *testing.T) {
	podName := "test-pod"
	fakePod := getFakePod(podName, "test-ns", nil, nil)
	fakeClient := fake.NewSimpleClientset(fakePod)

	fakeOptions := CmdOptions{}
	fakeOptions.kubeClient = fakeClient
	testIn := getTestInstance().in
	testOut := getTestInstance().out
	fakeOptions.In = testIn
	fakeOptions.Out = testOut

	// testing a pod that has not been interacted
	testOut.Reset()
	if err := fakeOptions.handleActionExtend([]corev1.Pod{*fakePod}); err != nil {
		t.Fatal(err)
	}
	expectedOut := fmt.Sprintf(noInteractionOfPodMsg, podName)
	checkMatches(t, expectedOut, testOut.String())

	// testing an interacted pod with no extension yet
	testOut.Reset()
	fakeTimestamp := strconv.FormatInt(time.Now().Unix(), 10)
	fakePod.SetLabels(map[string]string{podInteractionTimestampLabel: fakeTimestamp})
	testDuration := "30m"
	fakeOptions.extendDurationStr = testDuration
	if err := fakeOptions.handleActionExtend([]corev1.Pod{*fakePod}); err != nil {
		t.Fatal(err)
	}
	expectedOut = fmt.Sprintf(successExtensionOfPodWithDurationMsg, podName, testDuration)
	checkMatches(t, expectedOut, testOut.String())

	// testing an interacted pod with an existing duration
	// should output a warning, confirmation prompt, and a success message at the end
	testOut.Reset()
	testIn.WriteString("y\n")
	// manually set the extension here as 'handleActionExtend' does not return the updated pod
	fakePod.SetAnnotations(map[string]string{podExtendDurationAnnotate: testDuration})
	updatedDuration := "2h"
	fakeOptions.extendDurationStr = updatedDuration
	if err := fakeOptions.handleActionExtend([]corev1.Pod{*fakePod}); err != nil {
		t.Fatal(err)
	}
	expectedOverwriteWarning := fmt.Sprintf(extensionExistsOfPodWarningMsg, fakePod.Name, testDuration)
	expectedExtensionUpdate := fmt.Sprintf(successExtensionOfPodWithDurationMsg, podName, updatedDuration)
	expectedOutAll := []string{expectedOverwriteWarning, overwriteExtensionPromptMsg, expectedExtensionUpdate}
	checkStrContainsAll(t, expectedOutAll, testOut.String())
}

func TestGetPodInteraction(t *testing.T) {
	podName := "test-pop"
	labelsMap := map[string]string{
		podInteractorLabel:  "test-user-1",
		podTTLDurationLabel: "2h",
	}
	annotationsMap := map[string]string{
		podExtendDurationAnnotate:  "30m",
		podExtendRequesterAnnotate: "test-user-2",
		podTerminationTimeAnnotate: time.Now().String(),
	}
	fakePod := getFakePod(podName, "test-ns", labelsMap, annotationsMap)

	expect := PodInteractionInfo{
		podName:         podName,
		interactor:      labelsMap[podInteractorLabel],
		ttlDuration:     labelsMap[podTTLDurationLabel],
		extension:       annotationsMap[podExtendDurationAnnotate],
		requester:       annotationsMap[podExtendRequesterAnnotate],
		terminationTime: annotationsMap[podTerminationTimeAnnotate],
	}
	result := getPodInteractionInfo(*fakePod)
	checkMatches(t, expect, result)
}

func TestIsValidDuration(t *testing.T) {
	// testing invalid duration input
	invalidDuration := ""
	result := isValidDuration(invalidDuration)
	checkMatches(t, false, result)

	invalidDuration = "123"
	result = isValidDuration(invalidDuration)
	checkMatches(t, false, result)

	invalidDuration = "abc"
	result = isValidDuration(invalidDuration)
	checkMatches(t, false, result)

	// testing valid duration input
	validDuration := "60s"
	result = isValidDuration(validDuration)
	checkMatches(t, true, result)

	validDuration = "30m"
	result = isValidDuration(validDuration)
	checkMatches(t, true, result)

	validDuration = "2h"
	result = isValidDuration(validDuration)
	checkMatches(t, true, result)

	validDuration = "1d"
	result = isValidDuration(validDuration)
	checkMatches(t, true, result)
}

// Helpful vars and utility functions for testing

var instance *TestInstance
var once sync.Once

// TestInstance provides information requires to run the test
type TestInstance struct {
	cmd             *cobra.Command
	in, out, errOut *bytes.Buffer
}

// getTestInstance returns the singleton TestInstance
func getTestInstance() *TestInstance {
	once.Do(func() {
		streams, in, out, errOut := genericclioptions.NewTestIOStreams()
		cmd := NewCmdPi(streams)
		instance = &TestInstance{cmd, in, out, errOut}
	})

	return instance
}

// getFakePod returns a fake pod with the given metadata
func getFakePod(name, namespace string, labels, annotations map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}

// getAllValues returns all the values from the given map
func getAllValues(m map[string]string) []string {
	var result []string
	for _, val := range m {
		result = append(result, val)
	}

	return result
}

// checkErrMsg checks if the given error contains the expected error message
func checkErrMsg(t *testing.T, actualError error, expectedErrMsg string) {
	if actualError == nil {
		t.Fatalf("expecting an error message but error = nil")
	}
	checkMatches(t, expectedErrMsg, actualError.Error())
}

// checkMatches checks if the given two objects are identical
func checkMatches(t *testing.T, expect, result interface{}) {
	if expect != result {
		t.Fatalf("should return \"%s\", got \"%s\"\n", expect, result)
	}
}

// checkStrContainsAll checks if the result string contains all given substrings
func checkStrContainsAll(t *testing.T, substrings []string, result string) {
	for _, substr := range substrings {
		if !strings.Contains(result, substr) {
			t.Fatalf("result \"%s\" does not contain expected substring \"%s\"\n", result, substr)
		}
	}
}
