package plugin

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	cmdUseMsg     = "kubectl pi [command] [flags]"
	cmdShortMsg   = "Get pod interaction info or request an extension of its termination time"
	cmdExampleMsg = `
    # get interaction info of specified pod(s)
    kubectl pi get <pod-name-1> <pod-name-2> <...> -n POD_NAMESPACE

    # get interaction info of all pods under the given namespace
    kubectl pi get -n <pod-namespace> --all

    # extend termination time of interacted pod(s)
    kubectl pi extend -d <duration> <pod-name-1> <pod-name-2> <...> -n POD_NAMESPACE

    # extend termination time of all interacted pods under the given namespace
    kubectl pi extend -d <duration> -n <pod-namespace> --all
`

	cmdGetAction    = "get"
	cmdExtendAction = "extend"

	cmdArgsLengthError      = "expecting at least one argument"
	cmdInvalidActionError   = "expecting an action of either 'get' or 'extend' in the command"
	cmdInValidDurationError = "expecting an duration in the following format: 30s, 10m, 6h, 1d, etc"

	noPodReturnedOfNamespaceMsg          = "no pods returned under the namespace '%s'\n"
	noInteractionOfPodMsg                = "no interaction detected from the pod/%s\n"
	extensionExistsOfPodWarningMsg       = "Warning: pod/%s is already annotated with an extension=%s\n"
	overwriteExtensionPromptMsg          = "Please confirm to overwrite the existing extension"
	successExtensionOfPodWithDurationMsg = "Successfully extended the termination time of pod/%s with a duration=%s\n"

	defaultExtendDuration = "30m"

	// The following label/annotation names must match to the constants defined in controller/kube_helper.go file
	podInteractionTimestampLabel = "box.com/podInitialInteractionTimestamp"
	podInteractorLabel           = "box.com/podInteractorUsername"
	podTTLDurationLabel          = "box.com/podTTLDuration"
	podExtendDurationAnnotate    = "box.com/podExtendedDuration"
	podExtendRequesterAnnotate   = "box.com/podExtensionRequester"
	podTerminationTimeAnnotate   = "box.com/podTerminationTime"
)

// isValidAction returns if the given action is valid in the command
func isValidAction(action string) bool {
	action = strings.ToLower(action)

	return action == cmdGetAction || action == cmdExtendAction
}

// isValidDuration returns if the given duration is in valid format
func isValidDuration(duration string) bool {
	// example valid duration format: 30s, 20m, 6h, 1d
	validFormat := regexp.MustCompile(`^[0-9]+[smhd]$`)

	return validFormat.MatchString(duration)
}

// getPodInteractionInfo constructs a PodInteractionInfo by parsing the metadata of the given pod
func getPodInteractionInfo(pod corev1.Pod) PodInteractionInfo {
	labels := pod.GetLabels()
	annotations := pod.GetAnnotations()

	return PodInteractionInfo{
		podName:         pod.Name,
		interactor:      labels[podInteractorLabel],
		ttlDuration:     labels[podTTLDurationLabel],
		extension:       annotations[podExtendDurationAnnotate],
		requester:       annotations[podExtendRequesterAnnotate],
		terminationTime: annotations[podTerminationTimeAnnotate],
	}
}

// patchAnnotations will update a K8s pod with given metadata type and values stored from a map.
// It returns the updated pod if no errors encountered
func patchAnnotations(pod corev1.Pod, dataMap map[string]string, kubeClient kubernetes.Interface) (*corev1.Pod, error) {
	isEmpty := len(pod.GetAnnotations()) == 0
	var patchStrs []string
	if isEmpty {
		// The metadata type has to exist before patching specific key/val dataMap
		emptyPatch := getAnnotatedJsonPatchStr("", "")
		patchStrs = append(patchStrs, emptyPatch)
	}
	for key, val := range dataMap {
		patchStr := getAnnotatedJsonPatchStr(key, val)
		patchStrs = append(patchStrs, patchStr)
	}
	patchData := []byte(fmt.Sprintf("[%s]", strings.Join(patchStrs, ",")))

	return kubeClient.CoreV1().Pods(pod.Namespace).Patch(context.TODO(), pod.Name, types.JSONPatchType, patchData, metav1.PatchOptions{})
}

// getAnnotatedJsonPatchStr returns a Json patchAnnotations string from the given metadata type, key and value.
// It returns an empty patchAnnotations string of the metadata type if the specified key is empty
func getAnnotatedJsonPatchStr(key, val string) string {
	// return an empty patchAnnotations string of the specified metadata type if the given key is empty
	if key == "" {
		return "{\"op\":\"add\",\"path\":\"/metadata/annotations\",\"value\":{}}"
	}

	// replace invalid characters from key to satisfy Json patchAnnotations format
	key = strings.ReplaceAll(key, "~", "~0")
	key = strings.ReplaceAll(key, "/", "~1")

	return fmt.Sprintf("{\"op\":\"add\",\"path\":\"/metadata/annotations/%s\",\"value\":\"%s\"}", key, val)
}
