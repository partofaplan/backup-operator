# backup-operator

Backup Operator is a Kubernetes controller that captures API resources across the
cluster, serialises them into JSON, and archives the results. Each backup is
triggered by a `ClusterBackup` custom resource and can be tuned with namespace
filters, cluster-scope inclusion, and retention settings. The controller manages
leader election, health/ready probes, and ships a Helm chart for easy
installation.

## Getting Started

### Prerequisites
- go 1.24.0 or newer
- docker 17.03 or newer, with access to a registry your cluster can reach
- kubectl 1.11.3 or newer
- Access to a Kubernetes 1.11.3+ cluster

### Build and publish the controller image

Set the target registry/tag and use the make targets to build and push the
manager image. Replace the registry path with one that your cluster can pull
from.

```sh
export IMG=ghcr.io/<org>/backup-operator:v0.1.0
make docker-build IMG=$IMG
make docker-push IMG=$IMG
```

> Tip: for multi-architecture images use `make docker-buildx IMG=$IMG`.

### Deploy with Helm

A first-party chart lives in `deploy/helm/backup-operator`. It packages the
CRDs, RBAC objects, controller Deployment, and metrics service.

```sh
helm upgrade --install backup-operator ./deploy/helm/backup-operator \
  --namespace backup-operator --create-namespace \
  --set image.repository=${IMG%:*} --set image.tag=${IMG##*:}
```

Useful values to override:
- `image.repository`, `image.tag`, `image.pullPolicy`
- `leaderElection.enabled` and `leaderElection.namespace`
- `metrics.enabled`, `metrics.secure`, and `metrics.service.port`
- `resources`, `affinity`, `tolerations`, `extraEnv`, and `extraVolumes`

After the release succeeds, verify the CRD registration:

```sh
kubectl get crd clusterbackups.backup.backup.io
```

### Create a ClusterBackup resource

With the controller installed, apply a `ClusterBackup` manifest to initiate a
backup. Use the provided sample as a starting point:

```sh
kubectl apply -f config/samples/backup_v1alpha1_clusterbackup.yaml
```

Edit the sample to set a valid `storagePath` (for example `s3://bucket/path` or
`host:///var/lib/backup-operator`), adjust namespace filters, and tune
`retentionDays`/`maxArchives`. The status subresource will report progress,
completion time, and the archive file that was produced.

### Uninstall

```sh
helm uninstall backup-operator --namespace backup-operator
```

If you deployed with raw manifests, first delete any `ClusterBackup` resources,
then run `make uninstall` followed by `make undeploy`.

## Project Distribution

The legacy make targets (`make build-installer` and `kubebuilder edit
--plugins=helm/v1-alpha`) are retained for compatibility, but the Helm chart in
`deploy/helm/backup-operator` is the recommended distribution mechanism.

## Contributing

Run `make help` to inspect available development targets. Refer to the
[Kubebuilder documentation](https://book.kubebuilder.io/introduction.html) for
guide rails on extending controller-runtime projects.

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

