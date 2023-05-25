 # Cross-Resource Dependencies

* Owners: Hasan Turken (@turkenh)
* Reviewers: Crossplane Maintainers
* Status: Draft

## Background

## Goals

### Non-goals

## Proposal

### API

A Reference used only for deletion ordering:

```yaml
apiVersion: apiextensions.crossplane.io/v1alpha1
type: Reference
metadata:
  name: release-uses-cluster
spec:
  # A reference with type 'BlocksDeletion' prevents the 'to' (Cluster) resource
  # from being deleted until the 'from' resource (the Release) is gone.
  type: BlocksDeletion
  blocksDeletion:
    of:
      apiVersion: eks.upbound.io/v1beta1
      kind: Cluster
      name: my-cluster
    for:
      apiVersion: helm.crossplane.io/v1beta1
      kind: Release
      name: my-prometheus-chart
```

A reference used only to derive field values:

```yaml
apiVersion: apiextensions.crossplane.io/v1alpha1
type: Reference
metadata:
  name: subnet-to-vpc
spec:
  type: PatchesFieldPath
  patchesFieldPath:
    from:
      apiVersion: network.aws.crossplane.io/v1alpha2
      kind: VPC
      name: my-vpc
      fieldPath: metadata.annotations[crossplane.io/external-name]
    transforms:
      - type: string
        string:
          fmt: "id:%s"
    to:
      apiVersion: network.aws.crossplane.io/v1alpha2
      kind: Subnet
      name: my-subnet
      # Note the optional fieldPath is specified here and below.
      fieldPath: spec.forProvider.vpcId
```

```yaml
apiVersion: apiextensions.crossplane.io/v1alpha1
type: Reference
metadata:
  name: subnet-to-vpc
spec:
  type: PatchesWithCombine
  patchesWithCombine:
    variables:
      from:
        - apiVersion: network.aws.crossplane.io/v1alpha2
          kind: VPC
          name: my-vpc
          fieldPath: metadata.annotations[crossplane.io/external-name]
        - apiVersion: network.aws.crossplane.io/v1alpha2
          kind: VPC
          name: my-vpc
          fieldPath: metadata.annotations[crossplane.io/external-name]
    strategy: string
    string:
      fmt: "%s-%s"
    to:
      apiVersion: network.aws.crossplane.io/v1alpha2
      kind: Subnet
      name: my-subnet
      # Note the optional fieldPath is specified here and below.
      fieldPath: spec.forProvider.vpcId
```
