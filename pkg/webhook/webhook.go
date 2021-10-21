package webhook

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	"github.com/box/kube-exec-controller/pkg/controller"
)

var codec = serializer.NewCodecFactory(runtime.NewScheme())

const (
	PodExecAdmissionRequestKind   = "PodExecOptions"
	PodAttachAdmissionRequestKind = "PodAttachOptions"

	ImmutableLabelsDisallowMsg = "The following Pod labels cannot be updated or removed once set:"
	InvalidAnnotationsValueMsg = "The given annotation has an invalid value set in the Pod object:"
)

// Server handles admission requests received from K8s API-Server.
type Server struct {
	port              int
	tlsConfig         *tls.Config
	AllowedNamespaces map[string]bool
}

// NewServer sets up required configuration and returns a new Server object.
func NewServer(port int, certPath, keyPath, namespaceAllowlistRaw string) (*Server, error) {
	var tlsConf *tls.Config
	keyPair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}

	tlsConf = &tls.Config{
		Certificates: []tls.Certificate{keyPair},
	}

	return &Server{
		port:              port,
		tlsConfig:         tlsConf,
		AllowedNamespaces: parseNamespaceAllowlist(namespaceAllowlistRaw),
	}, nil
}

// Run will starts the webhook server listening to the specified paths.
func (s *Server) Run() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/liveness", handleLiveness)
	mux.HandleFunc("/health/readiness", handleReadiness)
	mux.HandleFunc("/admit-pod-interaction", s.AdmitPodInteraction)
	mux.HandleFunc("/admit-pod-update", s.AdmitPodUpdate)

	loggedHandler := loggingMiddleware()(mux)
	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.port),
		Handler:           loggedHandler,
		TLSConfig:         s.tlsConfig,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}

	return httpServer.ListenAndServeTLS("", "")
}

// AdmitPodInteraction handles an incoming request of interacting a Pod (by kubectl "exec" or "attach" command).
func (s *Server) AdmitPodInteraction(w http.ResponseWriter, r *http.Request) {
	admissionReview, err := parseIncomingRequest(r)
	if err != nil || admissionReview.Request == nil {
		zap.L().Error("Received a bad request when admitting Pod interaction", zap.Error(err))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	admissionRequest := admissionReview.Request

	// skip if a request contains any namespace in the predefined allow-list
	if s.AllowedNamespaces[admissionReview.Request.Namespace] {
		zap.L().Debug("Skipped as the request's namespace is in the predefined allow-list",
			zap.String("namespace", admissionRequest.Namespace),
		)
		writeAdmitResponse(w, http.StatusOK, admissionReview, true, "")
		return
	}

	// parse the request into an PodInteraction object and add it to channel for controller to process
	podInteraction, err := getPodInteractionStruct(admissionRequest)
	if err != nil {
		zap.L().Error("Unable to construct a PodInteraction struct from the admission request", zap.Error(err))
		writeAdmitResponse(w, http.StatusBadRequest, admissionReview, true, "")
		return
	}

	controller.PodInteractionCh <- podInteraction
	writeAdmitResponse(w, http.StatusOK, admissionReview, true, "")
}

// AdmitPodUpdate handles an incoming request of changing a Pod object.
func (s *Server) AdmitPodUpdate(w http.ResponseWriter, r *http.Request) {
	admissionReview, err := parseIncomingRequest(r)
	if err != nil || admissionReview.Request == nil {
		zap.L().Error("Received a bad request when admitting Pod update", zap.Error(err))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	admissionRequest := admissionReview.Request

	// skip if a request contains any namespace in the predefined allow-list.
	if s.AllowedNamespaces[admissionRequest.Namespace] {
		zap.L().Debug("Skipped as the request's namespace is in the predefined allow-list",
			zap.String("namespace", admissionRequest.Namespace),
		)
		writeAdmitResponse(w, http.StatusOK, admissionReview, true, "")
		return
	}

	// skip if the given Pod did not have label "PodInteractionTimestampLabel" set previously (not an interacted Pod)
	oldPod, err := getPodStruct(admissionRequest.OldObject.Raw)
	if err != nil {
		zap.L().Error("Error in getting Pod struct from admissionRequest.OldObject.Raw", zap.Error(err))
		writeAdmitResponse(w, http.StatusBadRequest, admissionReview, true, "")
		return
	}
	oldTimestamp, present := oldPod.Labels[controller.PodInteractionTimestampLabel]
	if !present {
		zap.L().Debug("Skipped as the request's Pod did not have label \"PodInteractedTimestampLabelKey\" set")
		writeAdmitResponse(w, http.StatusOK, admissionReview, true, "")
		return
	}

	// disallow if changing the Pod's label "PodInteractionTimestampLabel" or "PodTTLDurationLabel"
	// they are required to get a Pod's termination time and should not be changed once set
	pod, err := getPodStruct(admissionRequest.Object.Raw)
	if err != nil {
		zap.L().Error("Error in getting Pod struct from admitRequest.Object.Raw", zap.Error(err))
		w.WriteHeader(http.StatusBadRequest)
		writeAdmitResponse(w, http.StatusBadRequest, admissionReview, true, "")
		return
	}

	oldTTLDuration := oldPod.Labels[controller.PodTTLDurationLabel]
	if pod.Labels[controller.PodInteractionTimestampLabel] != oldTimestamp ||
		pod.Labels[controller.PodTTLDurationLabel] != oldTTLDuration {
		zap.L().Debug("Disallowed an request changing the PodInteractionTimestampLabel or PodTTLDurationLabel")
		message := fmt.Sprintln(ImmutableLabelsDisallowMsg, controller.PodInteractionTimestampLabel, controller.PodTTLDurationLabel)
		writeAdmitResponse(w, http.StatusOK, admissionReview, false, message)
		return
	}

	// check annotation change (for extending termination time)
	oldExtendDuration := oldPod.Annotations[controller.PodExtendDurationAnnotate]
	newExtendDuration := pod.Annotations[controller.PodExtendDurationAnnotate]
	if oldExtendDuration != newExtendDuration {
		// disallow if setting an invalid duration
		if _, err := time.ParseDuration(newExtendDuration); newExtendDuration != "" && err != nil {
			message := fmt.Sprintln(InvalidAnnotationsValueMsg, controller.PodExtendDurationAnnotate)
			writeAdmitResponse(w, http.StatusOK, admissionReview, false, message)
			return
		}

		podExtensionUpdate := controller.PodExtensionUpdate{
			Pod:      pod,
			Username: admissionRequest.UserInfo.Username,
		}
		controller.PodExtensionUpdateCh <- podExtensionUpdate
	}

	writeAdmitResponse(w, http.StatusOK, admissionReview, true, "")
}

// parseNamespaceAllowlist parses a comma-separated list of namespaces into a Map to have O(1) lookup time.
func parseNamespaceAllowlist(raw string) map[string]bool {
	namespaces := strings.TrimSpace(raw)
	resMap := map[string]bool{}

	for _, val := range strings.Split(namespaces, ",") {
		if ns := strings.TrimSpace(val); ns != "" {
			resMap[ns] = true
		}
	}

	return resMap
}

// writeAdmitResponse sends an allowed or disallowed response with additional message to the given admission request.
func writeAdmitResponse(w http.ResponseWriter, statusCode int, incomingReview admissionv1.AdmissionReview, isAllowed bool, message string) {
	w.Header().Set("Content-Type", "application/json")

	outgoingReview := admissionv1.AdmissionReview{
		TypeMeta: incomingReview.TypeMeta,
		Response: &admissionv1.AdmissionResponse{
			Allowed: isAllowed,
		},
	}

	if incomingReview.Request != nil {
		outgoingReview.Response.UID = incomingReview.Request.UID
	}

	// add a message with 403 HTTP status code when rejecting a request
	if !isAllowed {
		outgoingReview.Response.Result = &metav1.Status{
			Code:    http.StatusForbidden,
			Message: message,
		}
	}

	response, err := json.Marshal(outgoingReview)
	if err != nil {
		zap.L().Error("Error in marshaling outgoing admission review, returning 500", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if _, err = w.Write(response); err != nil {
		zap.L().Error("Error in writing an admit response, returning 500", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(statusCode)
}

// parseIncomingRequest parses the incoming request body and returns an admission.AdmissionReview object.
func parseIncomingRequest(r *http.Request) (admissionv1.AdmissionReview, error) {
	defer r.Body.Close()

	var incomingReview admissionv1.AdmissionReview
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return incomingReview, err
	}

	deserializer := codec.UniversalDeserializer()
	if _, _, err := deserializer.Decode(body, nil, &incomingReview); err != nil {
		return incomingReview, err
	}

	return incomingReview, nil
}

// getPodStruct returns a corev1.Pod from the given admitRequest.Object.Raw object.
func getPodStruct(fromAdmitRequestObjectRaw []byte) (corev1.Pod, error) {
	pod := corev1.Pod{}
	deserializer := codec.UniversalDeserializer()
	_, _, err := deserializer.Decode(fromAdmitRequestObjectRaw, nil, &pod)

	return pod, err
}

// getPodInteractionStruct parses the given admission request and returns a controller.PodInteraction object.
// The request must be either corev1.PodExecOptions or corev1.PodAttachOptions kind.
func getPodInteractionStruct(fromRequest *admissionv1.AdmissionRequest) (controller.PodInteraction, error) {
	var data map[string]interface{}
	err := json.Unmarshal(fromRequest.Object.Raw, &data)
	if err != nil {
		return controller.PodInteraction{}, err
	}

	kind := data["kind"].(string)
	if kind != PodExecAdmissionRequestKind && kind != PodAttachAdmissionRequestKind {
		return controller.PodInteraction{}, fmt.Errorf("invalid kind '%s' in the given admission request", kind)
	}

	container := data["container"].(string)

	// convert the raw command list from []interface to []string
	commandRaw := data["command"].([]interface{})
	commands := make([]string, len(commandRaw))
	for i, cr := range commandRaw {
		commands[i] = cr.(string)
	}

	return controller.PodInteraction{
		PodName:       fromRequest.Name,
		PodNamespace:  fromRequest.Namespace,
		ContainerName: container,
		Username:      fromRequest.UserInfo.Username,
		Commands:      commands,
		InitTime:      time.Now(),
	}, nil
}

// handleLiveness responds to a Kubernetes Liveness probe.
func handleLiveness(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	w.WriteHeader(http.StatusOK)
}

// handleReadiness responds to a Kubernetes Readiness probe.
func handleReadiness(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	w.WriteHeader(http.StatusOK)
}
