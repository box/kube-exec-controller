package plugin

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	// load the GCP authentication plug-in
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

// PodInteractionInfo contains all information of a pod interaction
type PodInteractionInfo struct {
	podName         string
	interactor      string
	ttlDuration     string
	extension       string
	requester       string
	terminationTime string
}

// CmdOptions provides context required to run the program
type CmdOptions struct {
	genericclioptions.IOStreams
	configFlags *genericclioptions.ConfigFlags
	// using kubernetes.Interface to allow testing by fake client
	kubeClient kubernetes.Interface

	args              []string
	action            string
	extendDurationStr string
	specifiedAll      bool

	podNames  []string
	namespace string
}

// NewCmdOptions provides an instance of CmdOptions
func NewCmdOptions(streams genericclioptions.IOStreams) *CmdOptions {
	return &CmdOptions{
		configFlags: genericclioptions.NewConfigFlags(false),
		IOStreams:   streams,
	}
}

// NewCmdPi provides a cobra command wrapping CmdOptions
func NewCmdPi(streams genericclioptions.IOStreams) *cobra.Command {
	opts := NewCmdOptions(streams)
	cmd := &cobra.Command{
		Use:          cmdUseMsg,
		Short:        cmdShortMsg,
		Example:      cmdExampleMsg,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := opts.Complete(args); err != nil {
				return err
			}

			if err := opts.Validate(); err != nil {
				return err
			}

			if err := opts.Run(); err != nil {
				return err
			}

			return nil
		},
	}

	// add "--duration/-d" flag to allow setting duration for pod extension request
	cmd.Flags().StringVarP(&opts.extendDurationStr, "duration", "d", defaultExtendDuration,
		fmt.Sprintf("a relative duration such as 5s, 2m, or 3h, default to %s", defaultExtendDuration))

	// add "--all/-a" flag to allow selecting all pods under the given namespace
	cmd.Flags().BoolVarP(&opts.specifiedAll, "all", "a", false,
		fmt.Sprintf("if present, select all pods under specified namespace (and ignore any given pod podName)"))

	// bind kubectl default options to the cmd flag set
	opts.configFlags.AddFlags(cmd.Flags())

	return cmd
}

// Complete sets all required context to run the command
func (o *CmdOptions) Complete(args []string) error {
	o.args = args
	if len(args) == 0 {
		return fmt.Errorf(cmdArgsLengthError)
	}

	o.action = args[0]
	o.podNames = args[1:]

	// select all pods if no specific pod name set
	if len(o.podNames) == 0 {
		o.specifiedAll = true
	}

	// get specified namespace from kubectl options
	var err error
	configLoader := o.configFlags.ToRawKubeConfigLoader()
	o.namespace, _, err = configLoader.Namespace()
	if err != nil {
		return err
	}

	// set up K8s client config
	clientConfig, err := configLoader.ClientConfig()
	if err != nil {
		return err
	}

	o.kubeClient, err = kubernetes.NewForConfig(clientConfig)
	if err != nil {
		return err
	}

	return nil
}

// Validate ensures that all required arguments and flag values are provided and validated
func (o *CmdOptions) Validate() error {
	// validate given action
	if len(o.action) == 0 || !isValidAction(o.action) {
		return fmt.Errorf(cmdInvalidActionError)
	}

	// validate the format of extended duration if set
	if o.action == cmdExtendAction && !isValidDuration(o.extendDurationStr) {
		return fmt.Errorf(cmdInValidDurationError)
	}

	return nil
}

// Run executes the command
func (o *CmdOptions) Run() error {
	pods, err := o.getSpecifiedPods()
	if err != nil {
		return err
	}

	if len(pods) == 0 {
		fmt.Println()
		return fmt.Errorf(noPodReturnedOfNamespaceMsg, o.namespace)
	}

	switch o.action {
	case cmdGetAction:
		return o.handleActionGet(pods)

	case cmdExtendAction:
		return o.handleActionExtend(pods)

	default:
		return fmt.Errorf("unknown action %s", o.action)
	}
}

// getSpecifiedPods returns list of pods specified in command options
func (o *CmdOptions) getSpecifiedPods() ([]corev1.Pod, error) {
	var specifiedPods []corev1.Pod
	if o.specifiedAll {
		// get all pods under the given namespace
		pods, err := o.kubeClient.CoreV1().Pods(o.namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return []corev1.Pod{}, err
		}

		specifiedPods = pods.Items
	} else {
		// get pod matching the specified pod name
		for _, podName := range o.podNames {
			pod, err := o.kubeClient.CoreV1().Pods(o.namespace).Get(context.TODO(), podName, metav1.GetOptions{})
			if err != nil {
				// continue to get other specified pods if the current one cannot be fetched
				fmt.Fprintf(o.Out, err.Error())
				continue
			}

			specifiedPods = append(specifiedPods, *pod)
		}
	}

	return specifiedPods, nil
}

// handleActionGet gets the pod interaction info and prints out the result in a formatted table
func (o *CmdOptions) handleActionGet(pods []corev1.Pod) error {
	var infoList []PodInteractionInfo
	for _, pod := range pods {
		infoList = append(infoList, getPodInteractionInfo(pod))
	}

	return o.printTable(infoList)
}

// handleActionExtend sets the requested extension to the specified pods
func (o *CmdOptions) handleActionExtend(pods []corev1.Pod) error {
	for _, pod := range pods {
		if err := o.setExtensionMetadata(pod); err != nil {
			return err
		}
	}

	return nil
}

// printTable prints pod interaction related info from the given PodInteractionInfo list
func (o *CmdOptions) printTable(infoList []PodInteractionInfo) error {
	w := new(tabwriter.Writer)
	// format in tab-separated columns with a tab stop of 8
	w.Init(o.Out, 0, 8, 2, '\t', 0)
	fmt.Fprintln(w, "POD_NAME\tINTERACTOR\tPOD_TTL\tEXTENSION\tEXTENSION_REQUESTER\tEVICTION_TIME")
	for _, info := range infoList {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s",
			info.podName,
			info.interactor,
			info.ttlDuration,
			info.extension,
			info.requester,
			info.terminationTime,
		)
		fmt.Fprintln(w)
	}

	return w.Flush()
}

// setExtensionMetadata adds metadata to the given pod with the extension related info
func (o *CmdOptions) setExtensionMetadata(pod corev1.Pod) error {
	// pod with no termination label (non-interacted pod)
	if _, hasTerminationLabel := pod.Labels[podInteractionTimestampLabel]; !hasTerminationLabel {
		fmt.Fprintf(o.Out, noInteractionOfPodMsg, pod.Name)

		return nil
	}

	// ask confirmation before overwriting an existing extension of a pod
	if extendedDuration, present := pod.Annotations[podExtendDurationAnnotate]; present {
		fmt.Fprintf(o.Out, extensionExistsOfPodWarningMsg, pod.Name, extendedDuration)
		confirmed, err := o.askConfirmation(overwriteExtensionPromptMsg)
		if err != nil {
			return err
		}

		if !confirmed {
			return nil
		}
	}

	// set metadata to the pod with requested extension
	// we do not add username here as it will be done by the admission controller in the cluster
	patchDataMap := map[string]string{
		podExtendDurationAnnotate: o.extendDurationStr,
	}
	if _, err := patchAnnotations(pod, patchDataMap, o.kubeClient); err != nil {
		return err
	}

	fmt.Fprintf(o.Out, successExtensionOfPodWithDurationMsg, pod.Name, o.extendDurationStr)

	return nil
}

// askConfirmation prompts users to confirm their action by typing "y" or "yes"
func (o *CmdOptions) askConfirmation(prompt string) (bool, error) {
	reader := bufio.NewReader(o.In)

	for {
		fmt.Fprintf(o.Out, "%s [y/n]: ", prompt)
		response, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}

		response = strings.ToLower(strings.TrimSpace(response))
		if response == "y" || response == "yes" {
			return true, nil
		} else if response == "n" || response == "no" {
			return false, nil
		} else {
			fmt.Fprintf(o.Out, "Invalid input, please try again\n")
		}
	}
}
