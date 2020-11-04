#!/bin/bash
#
# Copyright 2020 IBM Corporation
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

#
# Project roles and role bindings to another namespace
#

function help() {
    echo "authorize-namespace.sh - Authorize a namespace to be managable from another namespare through the NamespaceScope operator"
    echo "SYNTAX:"
    echo "authorize-namespace.sh [namespace | default current namespace] [-to namespacename | default ibm-common-services] [-delete]"
    echo "WHERE:"
    echo "  namespace : is the name of the namspece you wish to authorize.  This namespace MUST exist, "
    echo "              by default the current namespace is assumed"
    echo "  tonamespace : is the name of the namespace that you want to authorize to manage artifacts in this namespace."
    echo "                This namespace MUST exist.  The default is ibm-common-services".
    echo "                The NamepaceScope CR MUST be define in this namespace with the name namespacescope."
    echo "  -delete : Removes the ability for the tonamespace to manage artifacts in the namespace."    
    echo ""
    echo "You must be logged into the Openshift cluster from the oc command line"
    echo ""
}

#
# MAIN LOGIC
#

TARGETNS=""
TONS="ibm-common-services"
DELETE=0

while (( $# )); do
  case "$1" in
    -to|--to)
      if [ -n "$2" ] && [ ${2:0:1} != "-" ]; then
        TONS=$2
        shift 2
      else
        echo "Error: Argument for $1 is missing" >&2
        exit 1
      fi
      ;;
    -delete|--delete)
      DELETE=1
      shift 1
      ;;
    -*|--*=) # unsupported flags
      echo "Error: Unsupported flag $1" >&2
      help
      exit 1
      ;;
    *) # preserve positional arguments
      TARGETNS="$TARGETNS $1"
      shift
      ;;
  esac
done

#
# Validate parameters
#

if [ -z $TARGETNS ]; then
    TARGETNS=$(oc project -q)
    if [ $? -ne 0 ]; then
      echo "Error: You do not seem to be logged into Openshift" >&2
      help
      exit 1
    fi
fi

COUNT=$(echo $TARGETNS | wc -w)
if [ $COUNT -ne 1 ]; then
    echo "Invalid  namespace " $TARGETNS >&2
    help
    exit 1
fi

TARGETNS=${TARGETNS//[[:blank:]]/}

oc get ns $TARGETNS
if [ $? -ne 0 ]; then
    echo "Invalid  namespace " $TARGETNS >&2
    help
    exit 1
fi

oc get ns $TONS
if [ $? -ne 0 ]; then
    echo "Invalid  namespace " $TARGETNS >&2
    help
    exit 1
fi

if [ "$TARGETNS" == "$TONS" ]; then
  echo "Namespace and tonamespace canot be the same namespace."
  help
  exit 1
fi

if [ $DELETE -eq 1 ]; then
  echo "Deleteing authorization of namespace $TARGETNS to $TONS" >&2
else
  echo "Authorizing namespace $TARGETNS to $TONS" >&2
fi

#
# Delete permissions and update the list if needed
#
if [ $DELETE -ne 0 ]; then
  oc delete role -l projectedfrom=$TONS -n $TARGETNS
  oc delete rolebinding -l projectedfrom=$TONS -n $TARGETNS
  exit 0
fi


#
# Define a role for service accounts
#
cat <<EOF | oc apply -n $TARGETNS -f -
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: namespace-scope-client
  labels:
    projectedfrom: $TONS
rules:
- apiGroups:
  - "*"
  resources:
  - "*"
  verbs:
  - "*"
EOF

#
# Bind the service account in the TO namespace to the Role in the target namespace
#
cat <<EOF | oc apply -n $TARGETNS -f -
kind: RoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: namespace-scope-binding
  labels:
    projectedfrom: $TONS
subjects:
- kind: ServiceAccount
  name: ibm-namespace-scope-operator
  namespace: $TONS
roleRef:
  kind: Role
  name: namespace-scope-client
  apiGroup: rbac.authorization.k8s.io
EOF