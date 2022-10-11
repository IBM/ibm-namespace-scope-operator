# namespaceScope - Manage operator and operand authority across namespaces

This operator automates the extension of operator watch and service account permission scope to other namespaces in an openshift cluster, which is similar, but complementary to `OperatorGroups` that are available with Operator Lifecycle Manager (OLM).  By using the `NamespaceScope` operator with the `Own Namespace` `OperatorGroup` install mode, you can more easily manage the security domain of your Operators and the Operand namespaces.

The `NamespaceScope` operator runs in the namespace whose operator's `WATCH_NAMESPACES` and `Roles`/`RoleBindings` are to be extended to other namespaces (the Operand namespaces) as specified in a NamespaceScope CR.  `WATCH_NAMESPACES` is an enviromental variable that the Operator SDK uses in conjunction with OLM `ClusterServiceVersion` deployment to identify which namespaces to watch.  OLM will inject the namespaces into an annotation into the Operator's Deployment resource and then use the downward API to pass those values into the `WATCH_NAMESPACES` environment variable.  To avoid conflicts with OLM, the `NamespaceScope` operator instead stores the watched namespace list in a user-configurable `ConfigMap` and the Deployment instead mounts this `ConfigMap` as an environment variable.

A sample CR is below:

```
apiVersion: operator.ibm.com/v1
kind: NamespaceScope
metadata:
  name: common-service
spec:
  # Namespaces that are part of this scope
  namespaceMembers:
  - cp4i
  - default

  # ConfigMap name that will contain the list of namespaces to be watched
  configmapName: namespace-scope

  # Restart pods with the following labels when the namespace list changes
  restartLabels:
    intent: projected
    
  # Automatically inject the configmap into the ClusterServiceVersion, overriding the OLM OperatorGroup membership for those operators that have
  # the nss.operator.ibm.com/managed-operators annotation defined.  This annotation is a comma-separate list of OLM operator package names that should
  # be honored.  This should include the package of itself and any dependent operators that cannot be annotated (e.g. third-party operators)
  csvInjector:
    enable: true
  ```

- The **namespaceMembers** contains a list of other namespace in the cluster that:
  - should be watched by operators running in the current namespace
  - to which `Roles` and `RoleBindings` for `ServiceAccounts` in the current namespace should be authorized for `ServiceAccounts` in this namespace

- The **namespaceMembers** list ALWAYS contains the current namespace whether specifically listed or not (it is implicit)

- The **configmapName** identifies a `ConfigMap` that is created to contain a common-separated list of the namespaces to be watched in its **namespaces** key.  

- The **restartLabels** list specifies the labels for operator pods that are to be restarted when the namespace-scope list changes so they can reset their WATCH parameters.  The default label is "intent=projected". All operator Pods that are configured as above should also be labelled so that the operator will auto-restart them the configmap changes the list of namespaces to watch.  An example of this label is below.

    ```
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: secretman
    spec:
      replicas: 1
      selector:
        matchLabels:
          name: secretman
      template:
        metadata:
          labels:
            name: secretman
            intent: projected
        spec:
        ...
    ```
- The **csvInjector** (default is false / disabled) automatically modifies any `ClusterServiceVersion` that appears in the current namespace with the `nss.operator.ibm.com/managed-operators` annotation to consume the `NamespaceScope` operator's `ConfigMap` specified by `configmapName` instead of the traditional downward API syntax scaffolded by the Operator SDK, allowing uninstrumented Operators to read the `NamespaceScope` watch namespaces.

The following is an example of what is added and overridden in all `ClusterServiceVersion` resources in the namespace:

    ```
    ...
    # This annotation must be added to the CSV for injection to occur.  Include THIS operator package and any dependant packages.
    metadata:
      annotations:
        nss.operator.ibm.com/managed-operators: "my-operator1,my-thirdparty-operator"
    ...
    # This is injected
    env:
      - name: WATCH_NAMESPACE
        valueFrom:
          configMapKeyRef:
            name: namespace-scope
            key: namespaces
    ...
    ```

## How does it work

Namespace Scope Operator runs in `ibm-common-services` namespace with namespace admin permission.

When the `NamespaceScope` CR is created/updated, it will:

1. Create role/rolebinding with service accounts from the pods who have label selector `restartLabels`

    ```
    apiVersion: rbac.authorization.k8s.io/v1
    kind: Role
    metadata:
      name: namespacescope-managed-role-from-NS-CR
      namespace: FROM_namespaceMembers
    rules:
    - apiGroups:
      - "*"
      resources:
      - "*"
      verbs:
      - create
      - delete
      - get
      - list
      - patch
      - update
      - watch
      - deletecollection
    ---
    kind: RoleBinding
    apiVersion: rbac.authorization.k8s.io/v1
    metadata:
      name: namespacescope-managed-role-from-NS-CR
      namespace: FROM_namespaceMembers
    subjects:
    - kind: ServiceAccount
      name: GET_FROM_PODS_WHO_HAVE_restartLabels
      namespace: ibm-common-services
    roleRef:
      kind: Role
      name: namespacescope-managed-role-from-NS-CR
      apiGroup: rbac.authorization.k8s.io
    ```

2. Generate a ConfigMap with key `namespaces` and value is the comma separated `namespaceMembers`

    ```
    apiVersion: v1
    kind: ConfigMap
    metadata:
      name: namespace-scope
      namespace: ibm-common-services
    data:
      namespaces: default,cp4i
    ```

3. Restart the pods with label selector `restartLabels`

    ```
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: secretman
    spec:
      replicas: 1
      selector:
        matchLabels:
          name: secretman
      template:
        metadata:
          labels:
            name: secretman
            intent: projected
        spec:
        ...
    ```


## How to manually deploy the NamespaceScope Operator

NOTE: This operator is part of the IBM Common Services and will be automatically installed. Following commands are only applicable when you want to deploy it without IBM Common Services.

In this example, the `my-operators` namespace is the namespace that will contain your OLM-Deployed operators:

```
git clone https://github.com/IBM/ibm-namespace-scope-operator.git
cd ibm-namespace-scope-operator

oc create ns my-operators

oc apply -f deploy/operator.ibm.com_namespacescopes.yaml
oc -n my-operators apply -f deploy/service_account.yaml
oc -n my-operators apply -f deploy/role.yaml
oc -n my-operators apply -f deploy/role_binding.yaml
oc -n my-operators apply -f deploy/operator.yaml
```

Once the Operator is deployed, create a `NamespaceScope` custom resource (CR) in the Operators namespace to watch other Operand namespaces:

```
oc create ns my-operand-tenant1
oc create ns my-operand-tenant2

cat <<EOF | oc apply -n my-operators -f -
apiVersion: operator.ibm.com/v1
kind: NamespaceScope
metadata:
  name: my-operator
spec:
  # Namespaces that are part of this scope
  namespaceMembers:
  - my-operand-tenant1
  - my-operand-tenant1

  # ConfigMap name that will contain the list of namespaces to be watched
  configmapName: namespace-scope

  # Restart pods with the following labels when the namespace list changes
  restartLabels:
    intent: projected

  # Automatically inject the configmap into the ClusterServiceVersion, overriding the OLM OperatorGroup membership for those operators that have
  # the nss.operator.ibm.com/managed-operators annotation defined.  This annotation is a comma-separate list of OLM operator package names that should
  # be honored.  This should include the package of itself and any dependent operators that cannot be annotated (e.g. third-party operators)
  csvInjector:
    enable: true

EOF
```


## Authorization and Permissions

The **authorize-namespace.sh** script in the `scripts/` directory is used to set up roles and binding in a target namespace.

The syntax for the command is below:

```
authorize-namespace.sh - Authorize a namespace to be manageable from another namespace through the NamespaceScope operator

SYNTAX:
authorize-namespace.sh [namespace | default current namespace] [-to namespacename | default ibm-common-services] [-delete]
WHERE:
  namespace : is the name of the namespace you wish to authorize.  This namespace MUST exist
              by default the current namespace is assumed
  tonamespace : is the name of the namespace that you want to authorize to manage artifacts in this namespace.
                This namespace MUST exist.  The default is ibm-common-services.
                The NamespaceScope CR MUST be define in this namespace with the name namespacescope.
  -delete : Removes the ability for the tonamespace to manage artifacts in the namespace.

```

For example, if you want to grant the namespace scope permission of `common-service` to the service account in `ibm-common-services` namespace, you can use the following command

```bash
scripts/authorize-namespace.sh common-service
```

if you want to revoke this namespace scope permission, you can use the following command

```bash
scripts/authorize-namespace.sh common-service -delete
```

**NOTE:** You must have cluster administrator access permissions to execute the command.
