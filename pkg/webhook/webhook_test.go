package webhook_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	"github.com/box/kube-exec-controller/pkg/controller"
	"github.com/box/kube-exec-controller/pkg/webhook"
)

// TestAdmitPodInteraction tests webhook server admitting pod interaction requests
func TestAdmitPodInteraction(t *testing.T) {
	setupZapLogging(t)

	testNamespaceAllow := "test-namespace-allow"
	testNamespaceRegular := "test-namespace-regular"

	testCases := []struct {
		name                      string
		admissionReview           admissionv1.AdmissionReview
		expectedAdmissionResponse admissionv1.AdmissionResponse
		expectedPodInteraction    controller.PodInteraction
	}{
		{
			name: "Test-1 admit pod interaction under an allowed (exempt) namespace",
			admissionReview: admissionv1.AdmissionReview{
				Request: &admissionv1.AdmissionRequest{
					UID:       "test-uid-exempt",
					Namespace: testNamespaceAllow,
				},
			},
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				UID:     "test-uid-exempt",
				Allowed: true,
			},
			expectedPodInteraction: controller.PodInteraction{},
		},
		{
			name: "Test-2 admit pod interaction from 'kubectl exec' under a regular (non-exempt) namespace",
			admissionReview: admissionv1.AdmissionReview{
				Request: &admissionv1.AdmissionRequest{
					UID:       "test-uid-exec",
					Namespace: testNamespaceRegular,
					Name:      "test-pod-exec",
					UserInfo: authenticationv1.UserInfo{
						Username: "test-user-exec",
					},
					Object: runtime.RawExtension{
						Raw: []byte(fmt.Sprintf(`{"kind":"%s", "container": "test-container-exec", "command":["test-command-exec"]}`, webhook.PodExecAdmissionRequestKind))},
				},
			},
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				UID:     "test-uid-exec",
				Allowed: true,
			},
			expectedPodInteraction: controller.PodInteraction{
				PodNamespace:  testNamespaceRegular,
				PodName:       "test-pod-exec",
				Username:      "test-user-exec",
				ContainerName: "test-container-exec",
				Commands:      []string{"test-command-exec"},
			},
		},
		{
			name: "Test-3 admit pod interaction from 'kubectl attach' under a regular (non-exempt) namespace",
			admissionReview: admissionv1.AdmissionReview{
				Request: &admissionv1.AdmissionRequest{
					UID:       "test-uid-attach",
					Namespace: testNamespaceRegular,
					Name:      "test-pod-attach",
					UserInfo: authenticationv1.UserInfo{
						Username: "test-user-attach",
					},
					Object: runtime.RawExtension{
						Raw: []byte(fmt.Sprintf(`{"kind":"%s", "container": "test-container-attach", "command":["test-command-attach"]}`, webhook.PodAttachAdmissionRequestKind))},
				},
			},
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				UID:     "test-uid-attach",
				Allowed: true,
			},
			expectedPodInteraction: controller.PodInteraction{
				PodNamespace:  testNamespaceRegular,
				PodName:       "test-pod-attach",
				Username:      "test-user-attach",
				ContainerName: "test-container-attach",
				Commands:      []string{"test-command-attach"},
			},
		},
	}

	testServer := webhook.Server{
		AllowedNamespaces: map[string]bool{
			testNamespaceAllow: true,
		},
	}
	controller.PodInteractionCh = make(chan controller.PodInteraction)
	var receivedPodInteraction controller.PodInteraction

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			bytesIn, _ := json.Marshal(testCase.admissionReview)
			request, _ := http.NewRequest("POST", "", bytes.NewBuffer(bytesIn))
			responseRecorder := httptest.NewRecorder()
			handler := http.HandlerFunc(testServer.AdmitPodInteraction)
			// use a goroutine as the AdmitPodInteraction func could send values to channel
			go func() {
				handler.ServeHTTP(responseRecorder, request)
				// manually insert an empty value in channel to unblock the loop
				if reflect.DeepEqual(testCase.expectedPodInteraction, controller.PodInteraction{}) {
					controller.PodInteractionCh <- controller.PodInteraction{}
				}
			}()

			// check received PodInteraction struct and the admission review response
			receivedPodInteraction = <-controller.PodInteractionCh
			checkPodIntearactionObj(t, receivedPodInteraction, testCase.expectedPodInteraction)
			checkAdmissionReviewResponse(t, responseRecorder.Body, testCase.expectedAdmissionResponse)
		})
	}

	close(controller.PodInteractionCh)
}

// TestAdmitPodUpdate tests webhook server admitting pod update requests
func TestAdmitPodUpdate(t *testing.T) {
	setupZapLogging(t)

	testNamespaceAllow := "test-namespace-allow"
	testNamespaceRegular := "test-namespace-regular"

	testCases := []struct {
		name                       string
		admissionReview            admissionv1.AdmissionReview
		expectedAdmissionResponse  admissionv1.AdmissionResponse
		expectedPodExtensionUpdate controller.PodExtensionUpdate
	}{
		{
			name: "Test-1 admit pod update under an allowed (exempt) namespace",
			admissionReview: admissionv1.AdmissionReview{
				Request: &admissionv1.AdmissionRequest{
					UID:       "test-uid-exempt",
					Namespace: testNamespaceAllow,
				},
			},
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				UID:     "test-uid-exempt",
				Allowed: true,
			},
		},
		{
			name: "Test-2 admit pod update of a non-interacted pod",
			admissionReview: admissionv1.AdmissionReview{
				Request: &admissionv1.AdmissionRequest{
					UID:       "test-uid-non-interacted",
					Namespace: testNamespaceRegular,
					Name:      "test-pod-non-interacted",
					Object: runtime.RawExtension{
						Raw: getPodObjectRaw(
							map[string]string{
								controller.PodInteractionTimestampLabel: time.Now().String(),
							},
							nil,
						),
					},
				},
			},
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				UID:     "test-uid-non-interacted",
				Allowed: true,
			},
		},
		{
			name: "Test-3 admit pod update of changing interacted labels (disallowed)",
			admissionReview: admissionv1.AdmissionReview{
				Request: &admissionv1.AdmissionRequest{
					UID:       "test-uid-change-interacted-labels",
					Namespace: testNamespaceRegular,
					Name:      "test-pod-change-interacted-labels",
					Object: runtime.RawExtension{
						Raw: getPodObjectRaw(
							map[string]string{
								controller.PodInteractionTimestampLabel: time.Now().Add(time.Minute).String(),
							},
							nil,
						),
					},
					OldObject: runtime.RawExtension{
						Raw: getPodObjectRaw(
							map[string]string{
								controller.PodInteractionTimestampLabel: time.Now().String(),
							},
							nil,
						),
					},
				},
			},
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				UID:     "test-uid-change-interacted-labels",
				Allowed: false,
				Result: &metav1.Status{
					Code:    http.StatusForbidden,
					Message: webhook.ImmutableLabelsDisallowMsg,
				},
			},
		},
		{
			name: "Test-4 admit pod update of requesting an extension with invalid value set (disallowed)",
			admissionReview: admissionv1.AdmissionReview{
				Request: &admissionv1.AdmissionRequest{
					UID:       "test-uid-invalid-extension",
					Namespace: testNamespaceRegular,
					Name:      "test-pod-invalid-extension",
					Object: runtime.RawExtension{
						Raw: getPodObjectRaw(
							map[string]string{
								controller.PodInteractionTimestampLabel: time.Time{}.String(),
							},
							map[string]string{
								controller.PodExtendDurationAnnotate: "some-invalid-value",
							},
						),
					},
					OldObject: runtime.RawExtension{
						Raw: getPodObjectRaw(
							map[string]string{
								controller.PodInteractionTimestampLabel: time.Time{}.String(),
							},
							nil,
						),
					},
				},
			},
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				UID:     "test-uid-invalid-extension",
				Allowed: false,
				Result: &metav1.Status{
					Code:    http.StatusForbidden,
					Message: webhook.InvalidAnnotationsValueMsg,
				},
			},
		},
		{
			name: "Test-5 admit pod update of requesting a new valid extension",
			admissionReview: admissionv1.AdmissionReview{
				Request: &admissionv1.AdmissionRequest{
					UID:       "test-uid-valid-extension",
					Namespace: testNamespaceRegular,
					Name:      "test-pod-valid-extension",
					UserInfo: authenticationv1.UserInfo{
						Username: "test-user-name",
					},
					Object: runtime.RawExtension{
						Raw: getPodObjectRaw(
							map[string]string{
								controller.PodInteractionTimestampLabel: time.Time{}.String(),
							},
							map[string]string{
								controller.PodExtendDurationAnnotate: "2h",
							},
						),
					},
					OldObject: runtime.RawExtension{
						Raw: getPodObjectRaw(
							map[string]string{
								controller.PodInteractionTimestampLabel: time.Time{}.String(),
							},
							nil,
						),
					},
				},
			},
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				UID:     "test-uid-valid-extension",
				Allowed: true,
			},
			expectedPodExtensionUpdate: controller.PodExtensionUpdate{
				Pod: corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							controller.PodInteractionTimestampLabel: time.Time{}.String(),
						},
						Annotations: map[string]string{
							controller.PodExtendDurationAnnotate: "2h",
						},
					},
				},
				Username: "test-user-name",
			},
		},
		{
			name: "Test-6 admit pod update of changing an existing extension",
			admissionReview: admissionv1.AdmissionReview{
				Request: &admissionv1.AdmissionRequest{
					UID:       "test-uid-change-extension",
					Namespace: testNamespaceRegular,
					Name:      "test-pod-change-extension",
					UserInfo: authenticationv1.UserInfo{
						Username: "test-user-name",
					},
					Object: runtime.RawExtension{
						Raw: getPodObjectRaw(
							map[string]string{
								controller.PodInteractionTimestampLabel: time.Time{}.String(),
							},
							map[string]string{
								controller.PodExtendDurationAnnotate: "5h",
							},
						),
					},
					OldObject: runtime.RawExtension{
						Raw: getPodObjectRaw(
							map[string]string{
								controller.PodInteractionTimestampLabel: time.Time{}.String(),
							},
							map[string]string{
								controller.PodExtendDurationAnnotate: "2h",
							},
						),
					},
				},
			},
			expectedAdmissionResponse: admissionv1.AdmissionResponse{
				UID:     "test-uid-change-extension",
				Allowed: true,
			},
			expectedPodExtensionUpdate: controller.PodExtensionUpdate{
				Pod: corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							controller.PodInteractionTimestampLabel: time.Time{}.String(),
						},
						Annotations: map[string]string{
							controller.PodExtendDurationAnnotate: "5h",
						},
					},
				},
				Username: "test-user-name",
			},
		},
	}

	testServer := webhook.Server{
		AllowedNamespaces: map[string]bool{
			testNamespaceAllow: true,
		},
	}

	controller.PodExtensionUpdateCh = make(chan controller.PodExtensionUpdate)
	var receivedPodExtensionUpdate controller.PodExtensionUpdate

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			bytesIn, _ := json.Marshal(testCase.admissionReview)
			request, _ := http.NewRequest("POST", "", bytes.NewBuffer(bytesIn))
			responseRecorder := httptest.NewRecorder()
			handler := http.HandlerFunc(testServer.AdmitPodUpdate)
			// use a goroutine as the AdmitPodUpdate func could send values to channel
			go func() {
				handler.ServeHTTP(responseRecorder, request)
				// manually insert an empty value in channel to unblock the loop
				if reflect.DeepEqual(testCase.expectedPodExtensionUpdate, controller.PodExtensionUpdate{}) {
					controller.PodExtensionUpdateCh <- controller.PodExtensionUpdate{}
				}
			}()

			// check received PodExtensionUpdate struct and the admission review response
			receivedPodExtensionUpdate = <-controller.PodExtensionUpdateCh
			checkPodExtensionUpdateObj(t, receivedPodExtensionUpdate, testCase.expectedPodExtensionUpdate)
			// checkPodPodExtensionUpdateObj(t, receivedPodExtensionUpdate, testCase.expectedPodExtensionUpdate)
			checkAdmissionReviewResponse(t, responseRecorder.Body, testCase.expectedAdmissionResponse)
		})
	}

	close(controller.PodExtensionUpdateCh)
}

// setupZapLogging gives better visibility when running a test
func setupZapLogging(t *testing.T) {
	logger := zaptest.NewLogger(t)
	zap.ReplaceGlobals(logger)
}

// getPodObjectRaw constructs a new pod with the given labels and annotations and returns the encoded result
func getPodObjectRaw(labels, annotations map[string]string) []byte {
	pod := corev1.Pod{}
	pod.SetLabels(labels)
	pod.SetAnnotations(annotations)

	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	codec := serializer.NewCodecFactory(scheme).LegacyCodec(corev1.SchemeGroupVersion)
	output, _ := runtime.Encode(codec, pod.DeepCopyObject())
	return output
}

// checkAdmissionReviewResponse parses the given responseBody to AdmissionReview and compares it with the given AdmissionResponse
func checkAdmissionReviewResponse(t *testing.T, responseBody *bytes.Buffer, expectedResponse admissionv1.AdmissionResponse) {
	var reviewOut admissionv1.AdmissionReview
	bytesOut, err := ioutil.ReadAll(responseBody)
	if err != nil {
		t.Error("error reading admission review respsone:", err)
		return
	}
	err = json.Unmarshal(bytesOut, &reviewOut)
	if err != nil {
		t.Error("error un-marshaling admission review respsone:", err, bytesOut)
		return
	}

	actualResponse := reviewOut.Response
	if actualResponse == nil {
		t.Error("expecting response from outgoing review, got nil")
		return
	}
	if actualResponse.Allowed != expectedResponse.Allowed {
		t.Errorf("expected response Allowed: %t, got: %t", expectedResponse.Allowed, actualResponse.Allowed)
	}
	if actualResponse.UID != expectedResponse.UID {
		t.Errorf("expected response UID: %s, got: %s", expectedResponse.UID, actualResponse.UID)
	}
	// check AdmissionResponse.Result if expected
	if expectedResponse.Result != nil {
		if expectedResponse.Result.Code != actualResponse.Result.Code {
			t.Errorf("expected response Result.Code: %d, got %d", expectedResponse.Result.Code, actualResponse.Result.Code)
		}
		if !strings.Contains(actualResponse.Result.Message, expectedResponse.Result.Message) {
			t.Errorf("expected response Result.Message contains '%s', got '%s'", expectedResponse.Result.Message, actualResponse.Result.Message)
		}
	}
}

// checkPodIntearactionObj checks if the given two controller.PodInteraction objects are equal
func checkPodIntearactionObj(t *testing.T, actual, expected controller.PodInteraction) {
	// reset InitTime in both PodInteraction to compare them properly
	actual.InitTime = time.Time{}
	expected.InitTime = time.Time{}

	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("expected: %v, got: %v", expected, actual)
	}
}

// checkPodExtensionUpdateObj if the given two controller.PodExtensionUpdate objects are equal
func checkPodExtensionUpdateObj(t *testing.T, actual, expected controller.PodExtensionUpdate) {
	// return early if both PodExtensionUpdate are empty
	if reflect.DeepEqual(actual, expected) {
		return
	}

	if actual.Username != expected.Username {
		t.Errorf("expected username: %s, got: %s", expected.Username, actual.Username)
	}

	if !reflect.DeepEqual(actual.Pod.GetLabels(), expected.Pod.GetLabels()) {
		t.Errorf("expected pod lables: %v, got: %v", expected.Pod.GetLabels(), actual.Pod.GetLabels())
	}

	if !reflect.DeepEqual(actual.Pod.GetAnnotations(), expected.Pod.GetAnnotations()) {
		t.Errorf("expected pod annotations: %v, got: %v", expected.Pod.GetAnnotations(), actual.Pod.GetAnnotations())
	}
}
