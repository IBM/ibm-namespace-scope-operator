#!/usr/bin/env bash

#
# Copyright 2021 IBM Corporation
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

# This script needs to inputs
# The CSV version that is currently in dev

CURRENT_DEV_CSV=$1
NEW_DEV_CSV=$2
PREVIOUS_DEV_CSV=$3

if [ -z "$NEW_DEV_CSV" ]; then
	let NEW_DEV_CSV_Z=$(echo $CURRENT_DEV_CSV | cut -d '.' -f3)+1
	NEW_DEV_CSV=$(echo $CURRENT_DEV_CSV | gsed "s/\.[0-9][0-9]*$/\.$NEW_DEV_CSV_Z/")
fi
if [ -z "$PREVIOUS_DEV_CSV" ]; then
	let PREVIOUS_DEV_CSV_Z=$(echo $CURRENT_DEV_CSV | cut -d '.' -f3)-1
	PREVIOUS_DEV_CSV=$(echo $CURRENT_DEV_CSV | gsed "s/\.[0-9][0-9]*$/\.$PREVIOUS_DEV_CSV_Z/")
fi

CSV_PATH=bundle/manifests
RESTRICTED_CSV_PATH=bundle-restricted/manifests

# Update New CSV
# replace old CSV value with new one
gsed -i "s/$CURRENT_DEV_CSV/$NEW_DEV_CSV/g" $CSV_PATH/ibm-namespace-scope-operator.clusterserviceversion.yaml
gsed -i "s/$CURRENT_DEV_CSV/$NEW_DEV_CSV/g" $RESTRICTED_CSV_PATH/ibm-namespace-scope-operator-restricted.clusterserviceversion.yaml
TIME_STAMP="$(date '+%Y-%m-%dT%H:%M:%S'Z)"
gsed -i "s/2[0-9]*-[0-9]*-[0-9]*T[0-9]*:[0-9]*:[0-9]*Z/$TIME_STAMP/g" $CSV_PATH/ibm-namespace-scope-operator.clusterserviceversion.yaml
gsed -i "s/2[0-9]*-[0-9]*-[0-9]*T[0-9]*:[0-9]*:[0-9]*Z/$TIME_STAMP/g" $RESTRICTED_CSV_PATH/ibm-namespace-scope-operator-restricted.clusterserviceversion.yaml

echo "Updated New file with new CSV version"
gsed -i "s/$PREVIOUS_DEV_CSV/$CURRENT_DEV_CSV/g" $CSV_PATH/ibm-namespace-scope-operator.clusterserviceversion.yaml
gsed -i "s/$PREVIOUS_DEV_CSV/$CURRENT_DEV_CSV/g" $RESTRICTED_CSV_PATH/ibm-namespace-scope-operator-restricted.clusterserviceversion.yaml
echo "Updated the replaces version line"

#Update version.go to new dev version
gsed -i "s/$CURRENT_DEV_CSV/$NEW_DEV_CSV/" version/version.go
gsed -i "s/$CURRENT_DEV_CSV/$NEW_DEV_CSV/" Makefile
echo "Updated the version.go with new version"
