# NamspaceScope - Manage operator and operand authority across namespaces

This operator automates the extension of operator watch and service account permission scope to other namespaces in an openshift cluster.

The operator runs in the namespace whose operator WATCH statements and roles/rolebindings are to be extended to other namespaces as specified in a NamespaceScope CR.

A sample CR is below:

```
apiVersion: namespacescope.ibm.com/v1alpha1
kind: NamespaceScope
metadata:
  name: namespacescope
spec:
  # Namespaces that are part of this scope
  namespaceMembers:
  - cp4i
  - default

  # ConfigMap name that will contain the list of namespaces to be watched
  configmap-name: namespace-scope

  # Restart pods with the following labels when the namspace list changes
  restartLabels:
    intent: projected

  # Set the following to true to manaually manage permissions for the NamespaceScope operator to extend control over other namespaces
  # The operator may fail when trying to extend permissions to other namespaces, but the cluster administrator can correct this using the
  # authorize-namespace command.
  manualManagement: false
  ```

- The **namespace-members** contains a list of other namespace in the cluster that:
  - should be watched by operators running in the current namespace
  - to which roles and rolebindings for service accounts in the current namespace should be authorized for service accounts in this namespace

- The **namespace-members** list ALWAYS contains the current namespace whether specifically listed or not (it is implicit)

- The **projected-serviceaccounts** list can be specified to control which service accounts get projected into the target namespaces.  By default (if the list is empty) all service accounts the namespace (except "system:" service accounts) are projected.

- The **configmap-name** identifies a ConfigMap that is created to contain a common-separated list of the namespaces to be watched in its **namespaces** key.  All operators that want to participate in namespace extension should be configured to watch the key on this configmap.  An example of this is in the operator deployment fragment below (the latest operator SDK support watching multiple namespaces in a comma-separated list). The configmap is created and maintained ONLY by the NamespaceScope operator.

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

- The **restart-labels** list specifies the labels for operator pods that are to be restarted when the namespace-scope list changes so they can reset their WATCH parameters.  The default label is "intent=projected". All operator Pods that are configured as above should also be labelled so that the operator will auto-restart them the configmap changes the list of namespaces to watch.  An example of this label is below.

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

The operator is written in ansible, and roles that invoked when resources changes are in the watches.yaml file

## Authorization and Permissions

By default, the NamespaceScope operator will have cluster permissions that allow it to project roles and rolebindings into any namespace that is requested in the namepsacescope CR.  These are wildcarded permissions, so if customer does not trust deploying with these permissions, they can elect to manually extend the permission of the operator when needed.  The status for the NamespaceScope operator CR will indicate when namespaces scope failures are encountered and must be managed manually.

The **authorize-namespace.sh** script in the scripts/ directory is used to set up roles and binding in a target namespace, when customers have chosen manual authorization management. 

The syntax for the command is below:

```
authorize-namespace.sh - Authorize a namespace to be managable from another namespare through the NamespaceScope operator
SYNTAX:
authorize-namespace.sh [namespace | default current namespace] [-to namespacename | default ibm-common-services] [-delete]
WHERE:
  namespace : is the name of the namspece you wish to authorize.  This namespace MUST exist
              by default the current namespace is assumed
  tonamespace : is the name of the namespace that you want to authorize to manage artifacts in this namespace.
                This namespace MUST exist.  The default is ibm-common-services.
                The NamepaceScope CR MUST be define in this namespace with ythe name namespacescope.
  -delete : Removes the ability for the tonamespace to manage artifacts in the namespace.

```
The command will automatically update the NamespaceScope Custom Resource named "namespacescope" in the "-to" namespace and will fail if it does not exist.   You must have cluster administrator access permissions to execute the command.


## High Level Design

Namespace Scope Operator runs in Common Services namespace with namespace admin permission.

When customers install a Cloud Pak in a new namespace, they should authorize the new namespace admin role/rolebinding to Namespace Scope Operator and update the NamespaceScope CR to add the new namespace to the `namespace-members` list. We will provide a tool/script for customers to simplify the authorization.

Namespace Scope Operator will reconcile the NamespaceScope CR to append the new namespace to the ConfigMap and restart the operators who have `restart-labels`. At this time, the related operators will now watch the new namespace.

Cloud Pak creates the Common Services CRs(such as OperandRequest, Certificate, Issuer) in the new namespace, the Common Services operators reconcile these CRs to do relevant operations or create relevant resources.

As we can see, in the lifecycle of Common Services, there is no cluster permission required.


## An end to end use case

1. IBM Common Services is installed in `ibm-common-services` namespace

    * The Namespace Scope Operator will be also installed into `ibm-common-services` as it is one of the operators of IBM Common Services
    * A default NamespaceScope CR will be created with `ibm-common-services` in the `namesapce-members` list
    * Namespace Scope Operator will reconcile NamespaceScope CR to generate a `namespace-scope` ConfigMap with key `namespaces` and value `ibm-common-services`
    * Individual operators will mount (an an environment variable) the ConfigMap and watch `ibm-common-services` namespace

2. Customers create `cloudpak1` namespace

3. Customers authorize namespace admin role/rolebinding to Namespace Scope Operator and update NamespaceScope CR to append the `cloudpak1` into `namespace-members` list

    The Namespace Scope Operator will reconcile the update NamespaceScope CR to:

    * Update `namespace-scope` ConfigMap with key `namespaces` and value `ibm-common-services,cloudpak1`
    * Create namespace admin role/rolebinding for individual Common Services operators
    * Restart the individual Common Services operators who have `restart-labels`
    * At this time, the individual Common Services operators will watch `ibm-common-services` and `cloudpak1` namespaces

4. Customers install CloudPak1 in namespace `cloudpak1`

    * CloudPak1 creates OperandRequest, Certificate, Issuer in `cloudpak1` namespace
    * Individual Common Services operators reconcile OperandRequest, Certificate, Issuer to relevant operations or generate relevant resources in `cloudpak1` namespace


## TODO

- Correct pod restarting form the restart-labels, current hard-coded to "intent=projected"
- Watch all roles and rolebindings in the namespace and project them into the member-list when created or changed
- Watch all namespaces and remove them from the **namespaces-members** list if/when they are deleted
- Proper finalizer if/when the CR is deleted
- Create an OLM operator for this project
- Improve the documentation and test the operator though all happy and error paths
