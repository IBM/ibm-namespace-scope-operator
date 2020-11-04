# NamspaceScope - Manage operator and operand authority across namespaces

This operator automates the extension of operator watch and service account permission scope to other namespaces in an openshift cluster.

The operator runs in the namespace whose operator WATCH statements and roles/rolebindings are to be extended to other namespaces as specified in a NamespaceScope CR.

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

  # Restart pods with the following labels when the namspace list changes
  restartLabels:
    intent: projected
  ```

- The **namespaceMembers** contains a list of other namespace in the cluster that:
  - should be watched by operators running in the current namespace
  - to which roles and rolebindings for service accounts in the current namespace should be authorized for service accounts in this namespace

- The **namespaceMembers** list ALWAYS contains the current namespace whether specifically listed or not (it is implicit)

- The **configmapName** identifies a ConfigMap that is created to contain a common-separated list of the namespaces to be watched in its **namespaces** key.  All operators that want to participate in namespace extension should be configured to watch the key on this configmap.  An example of this is in the operator deployment fragment below (the latest operator SDK support watching multiple namespaces in a comma-separated list). The configmap is created and maintained ONLY by the NamespaceScope operator.

    ```
    ...
    env:
      - name: WATCH_NAMESPACE
        valueFrom:
          configMapKeyRef:
            name: namespace-scope
            key: namespaces
    ...
    ```

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


## How does it work

Namespace Scope Operator runs in `ibm-common-services` namespace with namespace admin permission.

When the `NamespaceScope` CR is created/updated, it will:

* Generate a ConfigMap with key `namespaces` and value is the comma separated `namespaceMembers`

    ```
    apiVersion: v1
    kind: ConfigMap
    metadata:
      name: namespace-scope
      namespace: ibm-common-services
    data:
      namespaces: default,cp4i
    ```
* Restart the pods with label selector `restartLabels`

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

* Create role/rolebinding with service accounts from the pods who have label selector `restartLabels`

    ```
    apiVersion: rbac.authorization.k8s.io/v1
    kind: Role
    metadata:
      name: namespacescope-managed-role-from-NS
      namespace: FROM_namespaceMembers
    rules:
    - apiGroups:
      - '*'
      resources:
      - '*'
      verbs:
      - '*'
    ---
    kind: RoleBinding
    apiVersion: rbac.authorization.k8s.io/v1
    metadata:
      name: namespacescope-managed-role-from-NS
      namespace: FROM_namespaceMembers
    subjects:
    - kind: ServiceAccount
      name: GET_FROM_PODS_WHO_HAVE_restartLabels
      namespace: ibm-common-services
    roleRef:
      kind: Role
      name: namespacescope-managed-role-from-NS
      apiGroup: rbac.authorization.k8s.io
    ```


## How to manually deploy it

NOTE: This operator is part of the IBM Common Services and will be automatically installed. Following commands are only applicable when you want to deploy it without IBM Common Services.

```
git clone https://github.com/IBM/ibm-namespace-scope-operator.git
cd ibm-namespace-scope-operator

oc create ns ibm-common-services

oc apply -f deploy/operator.ibm.com_namespacescopes.yaml
oc -n ibm-common-services apply -f deploy/service_account.yaml
oc -n ibm-common-services apply -f deploy/role.yaml
oc -n ibm-common-services apply -f deploy/role_binding.yaml
oc -n ibm-common-services apply -f deploy/operator.yaml

oc -n ibm-common-services apply -f deploy/cr.yaml
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

For example, if you want to grant namespace admin permission of `common-service` to the service account in `ibm-common-services` namespace, you can use the following command

```bash
scripts/authorize-namespace.sh common-service
```

**NOTE:** You must have cluster administrator access permissions to execute the command.
