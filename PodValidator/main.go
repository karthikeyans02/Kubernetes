package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	Appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func getClientWithoutWarnings(config *rest.Config) (*kubernetes.Clientset, error) {
	config = rest.CopyConfig(config)
	config.WarningHandler = rest.NoWarnings{}
	return kubernetes.NewForConfig(config)
}

func main() {
	args := os.Args
	namespace := args[1]
	deploymentName := args[2]

	userHomeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("error getting user home dir: %v\n", err)
		os.Exit(1)
	}
	kubeConfigPath := filepath.Join(userHomeDir, ".kube", "config")
	fmt.Printf("Using kubeconfig: %s\n", kubeConfigPath)

	kubeConfig, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		fmt.Printf("error getting Kubernetes config: %v\n", err)
		os.Exit(1)
	}

	clientset, err := getClientWithoutWarnings(kubeConfig)
	if err != nil {
		log.Fatalf("Error creating Kubernetes client: %v", err)
	}

	deployment, err := clientset.AppsV1().Deployments(namespace).Get(context.TODO(), deploymentName, metav1.GetOptions{})
	if err != nil {
		log.Fatalf("Error getting deployment: %v", err)
	}

	labelSelector := metav1.FormatLabelSelector(deployment.Spec.Selector)

	pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		log.Fatalf("Error getting pod: %v", err)

	}

	count := 6
	for count > 0 {
		if printDeploymentStatus(deployment) {
			fmt.Printf("\n\n\n------------------------------------------\n[INFO] Deployment Status [%v]:\n------------------------------------------\n", deploymentName)
			fmt.Printf("Deployment successfull.\n\n")
			count = 0
		} else if count == 1 {
			fmt.Printf("\n[ERROR] Deployment is not up yet, checking pod logs \n")
			printPodStatus(pods, clientset, namespace)
			fmt.Printf("\n\n\n------------------------------------------\n[Error] Deployment Status [%v]:\n------------------------------------------\n", deploymentName)
			log.Fatalf("Deployment failed.\n\n")
		} else {
			fmt.Printf("[WARN] Deployment is not up yet, trying again in 60 secs... \n")
			time.Sleep(2 * time.Second)
			count = count - 1
		}
	}
}

func printDeploymentStatus(deployment *Appsv1.Deployment) bool {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == "Available" && condition.Status == "True" {
			return true
		}
	}
	return false
}

func getPodlogs(podName string, container v1.ContainerStatus, namespace string, clientset *kubernetes.Clientset) {
	fmt.Printf("Conatiner[%v]:", container.Name)
	logOptions := &v1.PodLogOptions{
		Container: container.Name,
	}
	status, _ := json.MarshalIndent(container.State, "", "  ")
	fmt.Println(string(status))

	req := clientset.CoreV1().Pods(namespace).GetLogs(podName, logOptions)
	podLogs, err := req.Stream(context.TODO())
	if err != nil {
		fmt.Printf("Error getting logs: %v", err)
	}
	defer podLogs.Close()
	reader := bufio.NewReader(podLogs)
	lineCount := 0
	seenLines := make(map[string]bool)
	fmt.Printf("\n\n[NOTE] Reason for Error:\n\n")
	for {
		line, _, err := reader.ReadLine()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			fmt.Printf("Error reading logs: %v", err)
		}
		lineStr := string(line)
		if strings.Contains(strings.ToLower(string(line)), "error") && !strings.Contains(strings.ToLower(string(line)), "datadog") {
			if !seenLines[lineStr] {

				fmt.Println(lineStr)
				seenLines[lineStr] = true
				lineCount++
				if lineCount >= 10 {
					break
				}
			}
		}
	}
}

func printPodStatus(pods *v1.PodList, clientset *kubernetes.Clientset, namespace string) {

	for _, pod := range pods.Items {
		fmt.Printf("\n-------------------------------------------------\nPod status [%v]:\n-------------------------------------------------\n\n", pod.Name)
		for _, container := range pod.Status.ContainerStatuses {
			if container.State.Running == nil || !container.Ready {
				if container.State.Waiting != nil {
					if container.State.Waiting.Reason == "ImagePullBackOff" || container.State.Waiting.Reason == "ErrImagePull" {
						status, _ := json.MarshalIndent(container.State, "", "  ")
						fmt.Println(string(status))
						secretName := pod.Spec.ImagePullSecrets[0].Name
						_, err := clientset.CoreV1().Secrets(namespace).Get(context.TODO(), secretName, metav1.GetOptions{})
						if err != nil {
							fmt.Printf("\n\n[NOTE] Reason for ImagePullBackOff: Error getting secret %v: %v in namspace %v, please add them\n\n", secretName, err, namespace)
						} else {
							fmt.Printf("\n\n[NOTE] Reason for ImagePullBackOff:	Secret %v is present in namespace %v, this error could be due to expired or wrong values in the secret\n\n", secretName, namespace)
						}
					} else if container.State.Waiting.Reason == "CreateContainerConfigError" {
						status, _ := json.MarshalIndent(container.State, "", "  ")
						fmt.Println(string(status))
						if strings.Contains(container.State.Waiting.Message, "secret") {
							msg := "Check if the env block in deployment yaml has correct \"secretKeyRef\", also see the \"SecretStore\" if the secret is from vault"
							fmt.Printf("\n\n[NOTE] Reason for CreateContainerConfigError: %v\n", msg)
						} else {
							msg := "Check if the env block in deployment yaml has correct \"configMapKeyRef\" to the volume mount"
							fmt.Printf("\n\n[NOTE] Reason for CreateContainerConfigError: %v\n", msg)
						}
					} else {
						getPodlogs(pod.Name, container, namespace, clientset)
					}
				} else {
					getPodlogs(pod.Name, container, namespace, clientset)
				}
			} else {
				fmt.Printf("Container %v is in running state\n", container.Name)
			}
		}
	}
}
