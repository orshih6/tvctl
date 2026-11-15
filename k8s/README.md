# Deploying tvctl

The bot (`tvctl serve`) runs on the **prod** cluster, pinned to **`ori-minipc`**, the
worker at home on the same Wi-Fi as the TV, with `hostNetwork` (the TV can't reach pod
IPs, and it must fetch the photos the bot serves on port 8765). The ConfigMap and
Deployment are **deployed by Argo CD from `orshih6/manifest` `apps/tvctl`**: a push to
`main` builds `ghcr.io/orshih6/tvctl:sha-<commit>` and CI points that folder at it.
Change them there, not with `kubectl` (Argo reverts it). Hand-applied from here: the
Namespace, the PVC and the Secrets.

```bash
export KUBECONFIG=/Users/ori/myinternal/k0s/kubeconfig.yaml
```

## 1. Namespace, volume, pull secret

```bash
kubectl apply -f k8s/namespace.yaml -f k8s/pvc.yaml

kubectl get secret ghcr-pull-secret -n dms-admin -o json \
  | jq 'del(.metadata.namespace,.metadata.uid,.metadata.resourceVersion,.metadata.creationTimestamp,.metadata.ownerReferences)' \
  | kubectl apply -n tvctl -f -
```

## 2. Secrets

The bot is **@orshih_tvctl_bot**, used only by tvctl (two programs polling one bot steal
each other's messages). Its token is `TVCTL_BOT_TOKEN` in the workspace `.env`.

```bash
ENV=/Users/ori/myinternal/.env
kubectl create secret generic tvctl-secrets -n tvctl \
  --from-literal=TELEGRAM_BOT_TOKEN="$(grep '^TVCTL_BOT_TOKEN=' $ENV | cut -d= -f2-)" \
  --from-literal=TELEGRAM_CHAT_ID="$(grep '^TELEGRAM_CHAT_ID=' $ENV | cut -d= -f2-)" \
  --from-literal=CLAUDE_CODE_OAUTH_TOKEN="$(kubectl get secret upworkctl-secrets -n upworkctl -o jsonpath='{.data.CLAUDE_CODE_OAUTH_TOKEN}' | base64 -d)"
```

## 3. Pair with the TV (once)

The first time the bot presses a key, the TV shows **"Allow tvctl?"**. Press Allow with
the real remote; the token lands in the PVC (`/data/tv-token`) and it never asks again.
If it's ever denied, remove "tvctl" under the TV's Settings → General → External Device
Manager → Device Connection Manager → Device List, and press a button on /remote again.

## 4. Watch

```bash
kubectl -n argocd get application tvctl
kubectl -n tvctl logs deploy/tvctl -f
```

If the mini PC is `NotReady`, the bot is down until it's back (nothing else can reach
the TV). If the router gives the TV a new address, change `TV_HOST` in
`manifest/apps/tvctl/configmap.yaml`.
