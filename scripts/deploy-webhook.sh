#! /bin/bash
cd "$(dirname "$0")"

# Todo: Inject ip address of host

cat > webhook-configuration.yaml <<EOF
apiVersion: admissionregistration.k8s.io/v1beta1
kind: ValidatingWebhookConfiguration
metadata:
  name: "terraform.operator.validation"
webhooks:
- name: "terraform.operator.validation"
  rules:
  - apiGroups:   ["azurerm.tfb.local"]
    apiVersions: ["*"]
    operations:  ["CREATE", "UPDATE"]
    resources:   ["*"]
    scope:       "Namespaced"
  clientConfig:
    caBundle: $(cat ./../certs/ca.crt | base64 | tr -d '\n')
    url: "https://10.0.1.13/validate-tf-crd"
  admissionReviewVersions: ["v1beta1"]
  sideEffects: None
  timeoutSeconds: 30
EOF

kubectl apply -f webhook-configuration.yaml