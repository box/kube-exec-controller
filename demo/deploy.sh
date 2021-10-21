#!/bin/bash
set -e

# Create a Certificate Signing Request (CSR) for our admission webhook service
# See https://kubernetes.io/docs/tasks/tls/managing-tls-in-a-cluster/ for more detail
CSR_NAME='demo-csr.kube-exec-controller'
kubectl delete csr $CSR_NAME 2>/dev/null || true
rm -rf server*
# Install cfssl/cfssljson tools to generate the CSR required files
curl -o cfssl -sL https://github.com/cloudflare/cfssl/releases/download/v1.6.1/cfssl_1.6.1_darwin_amd64
curl -o cfssljson -sL https://github.com/cloudflare/cfssl/releases/download/v1.6.1/cfssljson_1.6.1_darwin_amd64
chmod +x cfssl cfssljson
cat demo/csr.json | ./cfssl genkey - | ./cfssljson -bare server
cat <<EOF | kubectl apply -f -
apiVersion: certificates.k8s.io/v1
kind: CertificateSigningRequest
metadata:
  name: $CSR_NAME
spec:
  request: $(cat server.csr | base64 | tr -d '\n')
  signerName: kubernetes.io/kubelet-serving
  usages:
  - digital signature
  - key encipherment
  - server auth
EOF

# Get the above CSR approved, download the issued certificate, and save it to a file
kubectl certificate approve $CSR_NAME
kubectl get csr $CSR_NAME -o jsonpath='{.status.certificate}' | base64 --decode >| server.crt

# Create a Namespace and a K8s Secret object containing the above TLS key-pair
NAMESPACE='kube-exec-controller'
kubectl delete namespace $NAMESPACE 2>/dev/null || true
kubectl create namespace $NAMESPACE
kubectl create secret tls demo-secret --cert=server.crt --key=server-key.pem -n $NAMESPACE

# Apply the demo app (Deployment, Service, and required RBAC objects)
kubectl apply -f demo/app.yaml

# Add the K8s cluster CA cert in our admission webhook configuration and apply it
clusterCA=$(kubectl config view --raw --minify --flatten -o jsonpath='{.clusters[].cluster.certificate-authority-data}')
webhookConfig=$(cat "demo/admission-webhook.yaml.template" | sed "s/{{CABUNDLE_VALUE}}/$clusterCA/g")
echo "$webhookConfig" | kubectl apply -f -
