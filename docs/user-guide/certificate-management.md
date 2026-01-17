# Certificate Management

Grove's webhook server requires TLS certificates to secure communication with the Kubernetes API server. This guide explains how to configure certificate management for your Grove deployment.

## Overview

Grove supports two certificate management modes:

| Mode | Description | Best For |
|------|-------------|----------|
| **Auto-provisioned** (default) | Grove automatically generates and manages self-signed certificates | Development, testing, quick setup |
| **External certificates** | You provide certificates from an external source (e.g., cert-manager, manual) | Production, enterprise PKI integration |

## Auto-Provisioned Certificates (Default)

By default, Grove's built-in cert-controller automatically:
- Generates self-signed CA and server certificates
- Stores them in a Kubernetes Secret
- Injects the CA bundle into webhook configurations
- Rotates certificates before expiry

This mode requires no additional configuration. Simply deploy Grove:

```bash
helm upgrade -i grove oci://ghcr.io/ai-dynamo/grove/grove-charts:<tag>
```

### How It Works

1. On startup, the cert-controller checks if the webhook certificate Secret exists
2. If missing or expired, it generates new certificates
3. The CA bundle is automatically injected into `ValidatingWebhookConfiguration` and `MutatingWebhookConfiguration` resources
4. Certificates are stored in the Secret specified by `config.server.webhooks.secretName` (default: `grove-webhook-server-cert`)

## External Certificate Management

For production environments, you may want to use certificates from your organization's PKI or a certificate manager like [cert-manager](https://cert-manager.io/).

### Prerequisites

Before deploying Grove with external certificates, ensure:
1. Your certificate Secret exists in the target namespace
2. The Secret contains the required keys: `tls.crt`, `tls.key`, and `ca.crt`
3. The certificate's Subject Alternative Names (SANs) include the webhook service DNS name

### Required Certificate SANs

The webhook server certificate must include these SANs:
```
grove-operator.<namespace>.svc
grove-operator.<namespace>.svc.cluster.local
```

Replace `<namespace>` with your deployment namespace (default: `default`).

### Configuration

To use external certificates, configure the following Helm values:

```yaml
config:
  server:
    webhooks:
      # Disable auto-provisioning
      autoProvision: false
      # Name of the Secret containing your certificates
      secretName: "grove-webhook-server-cert"

webhooks:
  # Base64-encoded CA certificate that signed the webhook server certificate
  caBundle: "<base64-encoded-ca-certificate>"
```

### Using cert-manager

[cert-manager](https://cert-manager.io/) is a popular choice for managing certificates in Kubernetes. Here's how to configure Grove with cert-manager.

#### Step 1: Install cert-manager

If not already installed:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.14.0/cert-manager.yaml
```

Wait for cert-manager to be ready:

```bash
kubectl wait --for=condition=Available deployment/cert-manager -n cert-manager --timeout=120s
kubectl wait --for=condition=Available deployment/cert-manager-webhook -n cert-manager --timeout=120s
```

#### Step 2: Create a Certificate Issuer

Create a self-signed issuer (or use your organization's issuer):

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: grove-selfsigned-issuer
  namespace: <namespace>  # Your Grove deployment namespace
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: grove-ca
  namespace: <namespace>
spec:
  isCA: true
  commonName: grove-ca
  secretName: grove-ca-secret
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: grove-selfsigned-issuer
    kind: Issuer
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: grove-ca-issuer
  namespace: <namespace>
spec:
  ca:
    secretName: grove-ca-secret
```

#### Step 3: Create a Certificate for the Webhook

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: grove-webhook-cert
  namespace: <namespace>
spec:
  secretName: grove-webhook-server-cert
  duration: 8760h  # 1 year
  renewBefore: 720h  # 30 days
  subject:
    organizations:
      - grove
  isCA: false
  privateKey:
    algorithm: RSA
    encoding: PKCS1
    size: 2048
  usages:
    - server auth
    - digital signature
    - key encipherment
  dnsNames:
    - grove-operator
    - grove-operator.<namespace>
    - grove-operator.<namespace>.svc
    - grove-operator.<namespace>.svc.cluster.local
  issuerRef:
    name: grove-ca-issuer
    kind: Issuer
```

#### Step 4: Configure Webhook Annotations

cert-manager can automatically inject the CA bundle into webhook configurations. Add these annotations to your Helm values:

```yaml
webhooks:
  podCliqueSetValidationWebhook:
    annotations:
      cert-manager.io/inject-ca-from: <namespace>/grove-webhook-cert
  podCliqueSetDefaultingWebhook:
    annotations:
      cert-manager.io/inject-ca-from: <namespace>/grove-webhook-cert
  authorizerWebhook:
    annotations:
      cert-manager.io/inject-ca-from: <namespace>/grove-webhook-cert
```

#### Step 5: Deploy Grove

```bash
helm upgrade -i grove oci://ghcr.io/ai-dynamo/grove/grove-charts:<tag> \
  --set config.server.webhooks.autoProvision=false \
  --set webhooks.podCliqueSetValidationWebhook.annotations."cert-manager\.io/inject-ca-from"="<namespace>/grove-webhook-cert" \
  --set webhooks.podCliqueSetDefaultingWebhook.annotations."cert-manager\.io/inject-ca-from"="<namespace>/grove-webhook-cert" \
  --set webhooks.authorizerWebhook.annotations."cert-manager\.io/inject-ca-from"="<namespace>/grove-webhook-cert"
```

Or create a `values.yaml` file with the configuration shown above and deploy:

```bash
helm upgrade -i grove oci://ghcr.io/ai-dynamo/grove/grove-charts:<tag> -f values.yaml
```

### Manual Certificate Management

If you prefer to manage certificates manually:

#### Step 1: Generate Certificates

```bash
# Create a CA
openssl genrsa -out ca.key 2048
openssl req -x509 -new -nodes -key ca.key -sha256 -days 365 \
  -out ca.crt -subj "/CN=grove-ca"

# Create server certificate
openssl genrsa -out tls.key 2048

# Create CSR config
cat > csr.conf <<EOF
[req]
default_bits = 2048
prompt = no
distinguished_name = dn
req_extensions = req_ext

[dn]
CN = grove-operator

[req_ext]
subjectAltName = @alt_names

[alt_names]
DNS.1 = grove-operator
DNS.2 = grove-operator.<namespace>
DNS.3 = grove-operator.<namespace>.svc
DNS.4 = grove-operator.<namespace>.svc.cluster.local
EOF

# Generate CSR and certificate
openssl req -new -key tls.key -out tls.csr -config csr.conf
openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out tls.crt -days 365 -extensions req_ext -extfile csr.conf
```

#### Step 2: Create the Secret

```bash
kubectl create secret generic grove-webhook-server-cert \
  --from-file=tls.crt=tls.crt \
  --from-file=tls.key=tls.key \
  --from-file=ca.crt=ca.crt \
  -n <namespace>
```

#### Step 3: Deploy Grove with the CA Bundle

```bash
CA_BUNDLE=$(cat ca.crt | base64 | tr -d '\n')

helm upgrade -i grove oci://ghcr.io/ai-dynamo/grove/grove-charts:<tag> \
  --set config.server.webhooks.autoProvision=false \
  --set webhooks.caBundle="${CA_BUNDLE}"
```

## Switching Between Modes

### From Auto-Provision to External

1. Set up your external certificate infrastructure (cert-manager or manual)
2. Update your Helm values to disable auto-provisioning
3. Upgrade the Grove deployment

```bash
helm upgrade grove oci://ghcr.io/ai-dynamo/grove/grove-charts:<tag> \
  --set config.server.webhooks.autoProvision=false \
  --set webhooks.caBundle="${CA_BUNDLE}"
```

### From External to Auto-Provision

1. Update your Helm values to enable auto-provisioning
2. Remove the `webhooks.caBundle` value (or set it to empty)
3. Upgrade the Grove deployment

```bash
helm upgrade grove oci://ghcr.io/ai-dynamo/grove/grove-charts:<tag> \
  --set config.server.webhooks.autoProvision=true \
  --set webhooks.caBundle=""
```

The cert-controller will generate new certificates on the next operator restart.

## Configuration Reference

| Helm Value | Type | Default | Description |
|------------|------|---------|-------------|
| `config.server.webhooks.autoProvision` | bool | `true` | Enable automatic certificate generation |
| `config.server.webhooks.secretName` | string | `grove-webhook-server-cert` | Name of the Secret containing certificates |
| `webhooks.caBundle` | string | `""` | Base64-encoded CA certificate for webhook configurations |
| `webhooks.podCliqueSetValidationWebhook.annotations` | map | `{}` | Annotations for the validation webhook (e.g., cert-manager injection) |
| `webhooks.podCliqueSetDefaultingWebhook.annotations` | map | `{}` | Annotations for the defaulting webhook |
| `webhooks.authorizerWebhook.annotations` | map | `{}` | Annotations for the authorizer webhook |

## Troubleshooting

### Webhook calls fail with certificate errors

**Symptom:** API server rejects webhook calls with TLS errors.

**Causes and solutions:**
1. **Missing CA bundle:** Ensure `webhooks.caBundle` is set when using external certificates
2. **Wrong SANs:** Verify the certificate includes the correct DNS names for your namespace
3. **Expired certificate:** Check certificate validity with `openssl x509 -in tls.crt -noout -dates`

### Operator fails to start

**Symptom:** Operator pod crashes with certificate-related errors.

**Causes and solutions:**
1. **Missing Secret:** When `autoProvision=false`, the Secret must exist before the operator starts
2. **Invalid certificate format:** Ensure `tls.crt` and `tls.key` are valid PEM-encoded files
3. **Missing `ca.crt`:** The Secret must contain all three keys: `tls.crt`, `tls.key`, and `ca.crt`

### cert-manager not injecting CA

**Symptom:** Webhook configurations have empty `caBundle` field.

**Causes and solutions:**
1. **Wrong annotation format:** Ensure the annotation value is `<namespace>/<certificate-name>`
2. **Certificate not ready:** Check `kubectl get certificate -n <namespace>` for status
3. **cert-manager not running:** Verify cert-manager pods are healthy

### Verifying Certificate Configuration

Check the webhook configuration:

```bash
kubectl get validatingwebhookconfiguration pcs-validation-webhook -o yaml | grep caBundle
```

Check the Secret contents:

```bash
kubectl get secret grove-webhook-server-cert -o jsonpath='{.data}' | jq 'keys'
```

Verify certificate details:

```bash
kubectl get secret grove-webhook-server-cert -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -text
```
