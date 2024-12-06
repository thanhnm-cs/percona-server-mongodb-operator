cat <<EOF | kubectl create -f -
apiVersion: v1
kind: Secret
metadata:
  name: pmm-secret
  labels:
    app.kubernetes.io/name: pmm
type: Opaque
data:
# base64 encoded password
# encode some password: `echo -n "admin" | base64`
  PMM_ADMIN_PASSWORD: YWRtaW4=
EOF




helm repo add percona https://percona.github.io/percona-helm-charts/


helm show values percona/pmm > pmm-values.yaml

helm install pmm \
-f pmm-values.yaml \
percona/pmm


