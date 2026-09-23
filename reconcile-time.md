# Technical Specification: Standardized Format for Representing Status

**State**: Ready

**Version**: v2

**Author(s)**: kai.ling.yan@ibm.com, mengdie.chu@ibm.com

**Reference**: 

Last Updated: Aug 6th, 2026

---

- [Introduction](#1-introduction)
  - [Concepts & Terminology](#11-concepts--terminology)
  - [Objective, Customer Expectations](#12-objective-customer-expectations)
  - [Requirements and Use Cases](#13-requirements-and-use-cases)
- [Overview](#2-overview-of-the-approach)
  - [Externals](#21-externals)
- [Guidance to CPD Services](#3-guidance-to-cpd-services)
  - [Standardization requirements](#31-standardization-requirements)
  - [Approaches to meeting the requirements](#32-approaches-to-meeting-the-requirements)
  - [Testing](#33-testing)
- [Q&A](#4-qa)
- [Operation Timing Tracking *(New Requirement)*](#5-operation-timing-tracking)
  - [Background and Motivation](#51-background-and-motivation)
  - [Limitations of cpd-cli-Based Timing](#52-limitations-of-cpd-cli-based-timing)
  - [Proposed Solution: operationTiming Status Field](#53-proposed-solution-operationtiming-status-field)
  - [Schema](#54-schema)
  - [Guidance to Service Teams](#55-guidance-to-service-teams)
  - [Implementation Notes by Operator Type](#56-implementation-notes-by-operator-type)
  - [Adoption Timeline](#57-adoption-timeline)


---

## 1. Introduction

### 1.1 Concepts & Terminology

Standardization of status involves defining a consistent format of displaying and representing the operation progress and troubleshooting tips that all CPD services will follow. This ensures that different services understand and interpret status outputs in the same way.

### 1.2 Objective, Customer Expectations

Progress indicators provide visibility into the state and progress of each CR, helping users understand what actions are currently being taken and if there are any issues. Customers will be able to track the progress of ongoing operations and receive formatted messages for handling any failures, including those related to installation, upgrades, shutdowns, and restarts etc, through the services' custom resources (CR). This is especially important when managing multiple CRs, as it allows users to monitor each resource independently and ensures a more transparent and user-friendly experience.

### 1.3 Requirements and Use Cases

Each service team will need to implement this in their service operator:
1. Each custom resource(CR) should have two sets of fields under `.status` to describe the status of the operand. 

The first key set, **progress** and **progressMessage** 

**progress**: Represents the percentage of completion towards the current operation. 

**progressMessage**: A general description of what is being executed. 

The second set, **reconcileHistory** and **status**

**reconcileHistory**: A list of messages, which may include meaningful errors or troubleshooting tips, with a maximum length of 3 entries.. The most recent failure is displayed as the first item in the array.

**status**: Represents the status of the current operation (ex. InProgess, Completed, Failed, shutdown, shutdownInProgress, etc).

2. Requirement for implementing the **progress** and **progressMessage** status
* The new key name must be progress and progressMessage.
* Must be implemented for various operations, including installation, upgrade, shutdown, restart, and any other processes.
* Different checkpoints should be established throughout the entire operation. Each team should include at least two checkpoints: one at 0% for a fresh start or when a new reconcile is triggered, and another at 100% for completion. It is strongly recommended to include 3 to 5 additional checkpoints in between.
* At each checkpoint, the process fields should be updated to reflect the new percentage and include a brief description of what was accomplished at that checkpoint in the CR. The percentage should start at 0% and progress to 100%.

3. Requirement for implementing the **reconcileHistory** and **status**
* The `reconcileHistory` is a list with maximum length of 3. 
* For each record, display the timestamp along with a meaningful error message or any useful tips that can assist the user in troubleshooting.
* The most recent failure is displayed as the first item in the array.
* The status field (which many services may have already implemented) indicates the current status of the operation, such as In Progress, Failed, Completed, or Shutdown, etc.
* After a successful reconciliation, please either clean up the `reconcileHistory` field or update the latest history to be "The last reconciliation was completed successfully."

The `get-cr-status` command in olm-utils will need to capture the new fields and display the result in terminal

---

## 2. Overview of the Approach 

### 2.1 Externals

The `get-cr-status` command in olm-utils gets the status of the components that are installed in the specified project.

Syntax
```bash
${CMD_PREFIX} get-cr-status \
[--cpd_instance_ns=<project name>] \
[--tethered_instance_ns=<project name>] \
[--cluster_component_ns=<project name>] \
[--components=<comma separated list of component names>]
```

The output should be
```
Component    CR-kind     CR-name    Namespace    Status     Progress    Progress Message                    Version    Creationtimestamp     Reconciled-version    Operator-info                  Operation-Duration
-----------  ----------  ---------  -----------  ---------  ----------  ----------------------------------  ---------  --------------------  --------------------  ---------------------------    ------------------
zen          ZenService  lite-cr    zen          Completed  100%        The Current Operation Is Completed  6.10.0      2026-07-07T16:03:01Z  6.10.0                 zen operator 6.10.0 build 11    16m30s
```

> **Note**: The `Operation-Duration` column shows the `totalDuration` from the most recent `operationTiming` entry in the service CR. This field is populated starting from SWH/CPD release 6.0 (July 2026) once service operators have adopted the `operationTiming` status field.

---

## 3. Guidance to CPD Services

### 3.1 Standardization requirements

All teams must comply with the requirements listed in [section 1.3](#13-requirements-and-use-cases) of this document.

In order for `get-cr-status` command to get the status information, each service team must ensure you have the below fields defined in global.yml
- status_field
- status_reconciled_version_field
- status_operator_info_field

### 3.2 Approaches to meeting the requirements

#### Sample Implementation of the `progress` and `progressMessage`.

**Note**: A function was created by Angad and Ruqhia Frozaan is merged to zen-ansible-utils, https://github.ibm.com/PrivateCloud/zen-ansible-utils/blob/6.0.x/tasks/set_cr_progress.yaml. Teams can take the advantage to adpot this function directly.
Sample
```yaml
- name: Updating progress
  include_tasks: "{{ path }}/zen-ansible-utils/tasks/set_cr_progress.yaml"
  vars:
    percentage: "60"
    message: "{{ message }}"
    api_version: "ccs.cpd.ibm.com/v1beta1"
    kind: CCS
    cr_name:  ccs-cr
  when: ansible_operator_meta.namespace is defined
```

1. Get the Percentage Shown at Each Checkpoint
* The % can be calculated dynamically during runtime or pre-defined.
* Checkpoints should be established after major deployments or resources that involve a long wait and require periodic checks.

2. Update the Service CR with the Incoming Percentage at Each Checkpoint
* Compare the incoming percentage with the current percentage shown in the CR before updating. Only update the percentage if the incoming value is higher or equal to the current percentage.
* Update the percentage to `0%` for a fresh reconcile loop or if the last reconciliation has reached 100%.

Expect output
```
status:
  progress: 100%
  progressMessage: The Current Operation Is Completed
```

Here is an example of how zen service implemented the percentage process field:

1. pre-defined the message and percentage for each checkpoint
```
- set_fact:
    progress_information:
      all:
        start:
          percentage: "0"
          msg: "New Reconcile Loop Begin"
        role_config: 
          percentage: "3"
          msg: "Finished Configure Variables"
        user_home_deploy:
          percentage: "4"
          msg: "Finished User-Home-PVC Deployment"     
        metastore_edb:
          percentage: "16"
          msg: "Finished Zen-Metastore edb" 
        role_0010:
          percentage: "35"
          msg: "Finished Role 0010-infra"
        zenwatcher_deploy:
          percentage: "40"
          msg: "Finished Zen-Watcher Deployment" 
        role_0020:
          percentage: "51"
          msg: "Finished Role 0020-core"
        role_0030:
          percentage: "66"
          msg: "Finished Role 0030-gateway"
        role_zen_adv: 
          percentage: "99"
          msg: "Finished Role zen-adv"
        end:
          percentage: "100"
          msg: "The Current Operation Is Completed"
```

2. Check whether the status should be updated
Example:
* Compare the incoming and current percentage. [Source] https://github.ibm.com/PrivateCloud/lite-ansible/blob/6.0.x/playbooks/set_zen_cr_progress.yaml#L113

The sample code below does not fit for all services, please use with caution.
```
# get cr info
- name: "{{ namespace }} Register info of current zenservice CR"
  kubernetes.core.k8s_info:
    api_version: "{{ api_version }}"
    kind: "{{ cr_kind }}"
    name: "{{ cr_name }}"
    namespace: {{ namespace }}
  register: cr_info

- name: "{{ namespace }} Display current cr info"
  debug: 
    var: cr_info
  when: cr_info is defined

- fail:
    msg: " {{ component }} CR is not found in namespace {{ namespace }}"
  when: >
    (cr_info is not defined) or 
    (cr_info is defined and cr_info.resources is not defined) or
    (cr_info is defined and cr_info.resources is defined and (cr_info.resources | length < 1))

- set_fact:
    update_progress: "{{ cr_info.resources[0].status.progress | default('Not available', true) }}"
    update_message: "{{ cr_info.resources[0].status.progressMessage | default('Not available', true) }}"

- name: "{{ namespace }} Get current percentage"
  set_fact:
    old_percent: "{{ update_progress.split('%').0 }}"

- name: "{{ namespace }} Retrieve new percentage"
  set_fact:
    new_percent: "{{ progress_information.all[role].percentage }}"
    new_msg: "{{ progress_information.all[role].msg }}"

- name: "{{ namespace }} Set new percentage when new > old"
  set_fact:
    update_progress: "{{ new_percent }}%"
    update_message: "{{ new_msg }}"
  when: (new_percent|int > old_percent|int)

- name: "{{ namespace }} Set to default value on restart"
  set_fact:
    update_progress: "{{ progress_information.all.start.percentage }}%"
    update_message: "{{ progress_information.all.start.msg }}"
  when: (old_percent|int==100) or (old_percent == "Not available") or (old_percent == "Not available for the current release")

- name: Update {{ component }} CR
  operator_sdk.util.k8s_status:
    api_version: "{{ api_version }}"
    kind: "{{ cr_kind }}"
    name: "{{ cr_name }}"
    namespace: "{{ namespace }}"
    status:
      progress: "{{ update_progress }}"
      progressMessage: "{{ update_message }}"
```

Zenservice will update the CR if any of the following is met
- the incoming % > current %
- the current % is 100%
- the current % is not available

Each service team might has other restrictions, please include them all. 

3. The percentage will be shown under `.status.progress` and the description will be shown under `.status.progressMessage` in the service CR
```
status:
  progress: 3%
  progressMessage: Finished Configure Variables
```

#### Sample of the `reconcileHistory` and `status`

**Note**: A function was created by Angad and Ruqhia Frozaan is merged to zen-ansible-utils, https://github.ibm.com/PrivateCloud/zen-ansible-utils/blob/6.0.x/tasks/set_reconcileHistory.yaml. Teams can take the advantage to adpot this function directly.
Sample
```yaml
- name: Updating reconcileHistory
  include_tasks: "{{ path }}/zen-ansible-utils/tasks/set_reconcileHistory.yaml"
  vars:
    new_error: "2024-08-15T15:16:25.5NZ Fail to wait deployment, please check deployment in namespace sample."
    api_version: "ccs.cpd.ibm.com/v1beta1"
    kind: CCS
    cr_name:  ccs-cr
  when: ansible_operator_meta.namespace is defined
```

Example code written in ansible

```yaml
  vars:
    current_error_list:
      - Third error msg
      - Second error msg
      - First error msg

  tasks:
    - name: Build error message
      set_fact:
        new_error: 
          - "{{ lookup('pipe','date +%Y-%m-%dT%H:%M:%S.%5NZ') + ' Fail to wait for deployment, please check deployment in namespace sample.' }}"
  
    - name: Add new error to the current list
      set_fact:
        current_error_list: "{{ new_error + current_error_list }}"
  
    - name: Remove last element from the list
      set_fact: 
        current_error_list: |
          {% set _ = temp_list.pop(value) %}
          {{ temp_list }}
      when: current_error_list | length >3
      vars:
        temp_list: "{{ current_error_list }}"
        value: 3

    - name: Set zen operator status to the InProgress
      operator_sdk.util.k8s_status:
        api_version: "zen.cpd.ibm.com/v1"
        kind: ZenService
        name: lite-cr
        namespace: zen
        status:
          reconcileHistory: "{{ current_error_list }}"

```

Expect output
```
status:
  reconcileHistory:
  - 2024-08-15T15:16:25.5NZ Fail to wait deployment, please check deployment in namespace
    sample.
  - Third error msg
  - Second error msg
  zenStatus: Failed
```

**After a successful reconcile loop, service team can choose one of the below implementation for the reconcileHistory**
1. Remove the field `reconcileHistory` after a successful reconcile loop
Here is the sample code:
```
    - name: Remove the reconcileHistory after a successful run
      operator_sdk.util.k8s_status:
        api_version: "zen.cpd.ibm.com/v1"
        kind: ZenService
        name: lite-cr
        namespace: zen
        replace_lists: true
        status:
          reconcileHistory: null
```
Please note that `reconcileHistory` must be a list in order to be removed by setting `replace_lists` to `true`.

2. Update the latest history to be "The last reconciliation was completed successfully."
Expect output
```
status:
  reconcileHistory:
  - 2024-08-25T17:15:25.5NZ The last reconciliation was completed successfully.
  - 2024-08-15T15:16:25.5NZ Fail to wait deployment, please check deployment in namespace
    sample.
  - 2024-08-14T10:18:20.5NZ Third error msg
  zenStatus: Completed
```

Reference: https://galaxy.ansible.com/ui/repo/published/operator_sdk/util/content/module/k8s_status/

### 3.3 Testing

Tests need to be done within service operator
1. status set to 0% when a new operation begins or the last reconcile has reached 100%
2. status updates if the incoming percentage is higher than the current percentage
3. status does not update if the incoming percentage is less than the current percentage

Tests need to be done using olm-utils
1. run the `get-cr-status` command to make sure the status of the operand can be retrieved

## 4. Q&A
- Q: If we already have `.status.conditions` providing generic information about the previous Ansible run, do we still need to implement the reconcileHistory?

    A: Yes, you still need to implement `reconcileHistory`. The drawback of relying on `.status.conditions` is that the error messages provided are often not detailed enough for customers or support teams to fully understand the issue and how to resolve it. Especially, when errors occur within a loop

- If my service has multiple CRs, do I need to implement the progress indicators for all the CRs?

    A: It depends on the situation. As a general guideline, ensure that any CR(s) defined with `cr_kind` in `global.yml` have progress indicators implemented. For example, if a service generates three CRs upon installation and only one of those `cr_kind` is visible in olm-utils, the requirement is to implement the feature for the visible one. It is also highly recommended to implement this feature for all CRs.

---

## 5. Operation Timing Tracking

### 5.1 Background and Motivation

**This is a new requirement introduced in SWH/CPD release 6.0, effective July 2026.**

There is a requirement to track service install, upgrade, and patch performance for reporting and comparison across releases. This information is valuable for measuring improvements and responding to customer and management requests.

> **📖 Terminology: "operation" vs. "reconcile loop"**
> Throughout this section, **operation** refers to a single end-to-end SWH/CPD activity — install, upgrade, or patch — as initiated by cpd-cli and driven to completion (or failure) by the service operator. One operation maps to **one entry** in `operationTiming`.
>
> For operators that **do not use requeue**, one operation maps directly to one reconcile-loop iteration. For operators that **enable requeue**, a single operation may span multiple reconcile-loop iterations — service teams in this case must ensure the `operationTiming` entry is written only when the operation concludes, not on every requeue.


### 5.2 Limitations of cpd-cli-Based Timing

cpd-cli has fundamental structural constraints that make it unsuitable as the sole source of timing data:

**a) Timing is only available during a live run**
cpd-cli can only measure duration in real time while the command is actively running. There is no mechanism for it to retroactively determine how long a component's install, upgrade, or patch took.

**b) Success case works today**
When all components install or upgrade successfully, the existing command outputs a duration CSV table summarizing timing per component.

**c) Failure case breaks tracking — and cannot be fixed within cpd-cli's design**
If any component fails, the command exits immediately and timing tracking is terminated. cpd-cli does not retry because:
- There is no guarantee a retry will resolve the underlying failure.
- Continuing to wait wastes customer time when the outcome is uncertain.

As a result, cpd-cli cannot provide timing for other components once any component fails. This is a structural limitation, not a gap in the current implementation.

**d) Internal services are invisible to cpd-cli**
cpd-cli never creates or upgrades the CR for internal services, so it has no way to track their performance at all.

**e) Timing accuracy is inherently limited**
Even for external services, there is an inherent gap between the actual operation start/end time on the service side and what cpd-cli observes. cpd-cli can only infer start/end based on when it detects a status change in the service CR — not the actual moment the operation began or completed.

### 5.3 Proposed Solution: operationTiming Status Field

To achieve accurate tracking for all components,regardless of whether cpd-cli is running or whether the overall install/upgrade completes successfully, the recommended approach is to record timing directly in the service CR.

Each service operator must add a dedicated `operationTiming` field under `.status`. The field stores the **latest five operation timing entries**, with the **most recent entry as the first item** in the array. Older entries follow in descending order. When a sixth operation completes, the oldest entry is dropped. This provides:


- **Accuracy** — timing comes directly from the source of truth (the service operator itself), eliminating the detection-lag issue.
- **Completeness** — internal services get tracked too, since this does not depend on cpd-cli creating or upgrading anything.
- **Serviceability** — retaining five entries gives support teams a recent history of consecutive operations to spot patterns such as gradual performance regressions or repeated pre-check failures, without requiring log access. Moreover, customers can also self-serve a simple `oc get <cr> -o yaml` surfaces the full timing history in the CR, letting them diagnose slow installs or upgrades before opening a support ticket.
- **Resilience** — timing data persists and remains queryable even if a component fails or the overall cpd-cli run terminates early.
- **Reusability** — the data becomes available for cross-release comparison, not just single-run reporting.


Accordingly, in cpd-cli, the `get-cr-status` command in olm-utils will be updated to surface a new **Operation-Duration** column, showing the `totalDuration` from the most recent `operationTiming` entry. This gives customers a quick, at-a-glance view of how long the latest operation took without needing to inspect the CR directly.

```
Component    CR-kind     CR-name    Namespace    Status     Progress    Progress Message                    Version    Creationtimestamp     Reconciled-version    Operator-info                  Operation-Duration
-----------  ----------  ---------  -----------  ---------  ----------  ----------------------------------  ---------  --------------------  --------------------  ---------------------------    ------------------
zen          ZenService  lite-cr    zen          Completed  100%        The Current Operation Is Completed  6.10.0      2026-07-07T16:03:01Z  6.10.0                 zen operator 6.10.0 build 11    16m30s
```

### 5.4 Schema

Each entry in `operationTiming` records the following fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `startTime` | timestamp | Yes | Time when the operation began |
| `endTime` | timestamp | Yes | Time when the operation ended |
| `totalDuration` | string | Yes | Computed as `endTime - startTime` |
| `phase` | string | Yes | Final phase of the operation (e.g. `Completed`, `Failed`, or other status value) |
| `dependencyTime` | list | No | Omit if the service has no dependencies (see below) |

The list retains the **latest five** entries. The **most recent entry is always the first item** in the array; older entries follow in descending order. When a sixth operation completes, the oldest entry is removed. Five entries are kept intentionally, balancing serviceability (giving support teams a recent history of consecutive operations to identify patterns such as regressions or repeated failures) against CR size.

**dependencyTime** is a list of objects, one per immediate dependency:

| Field | Type | Description |
|---|---|---|
| `component` | string | Name of the dependency component |
| `startTime` | timestamp | Time when the operator began waiting for this dependency |
| `readyTime` | timestamp | Time at which that dependency reached a ready state |
| `dependencyDuration` | string | Computed as `readyTime - startTime` for this dependency |

Only **first-level (immediate) dependencies** are reported. Based on the operator dependency management design for non-OLM installs, a service operator only waits for its direct dependencies, not transitive ones. For example, `watsonx_ai` waits for `ws`, but does not need to wait for `ccs` — even though `ws` itself waits for `ccs`. This keeps the list actionable for root-causing slow installs without introducing noise from indirect dependencies.

Additionally, for service operators that have logic to deploy **internal services** as part of their own operation, the ready time of those internal services must also be included as entries in the `dependencyTime` list. This ensures their deployment time is visible and attributable, rather than hidden inside the parent service's total duration.

The reason `dependencyTime` is a list rather than a single value is to:
1. Support services with zero, one, or multiple dependencies using a uniform structure.
2. Make it easy to see exactly which dependency (including any internal services deployed inline by the operator) took the longest to become ready, so teams can quickly narrow down the cause of a slow install or upgrade without guesswork.
3. Reveal wait efficiency gaps in the consumer operator. For example, if `ccs` reports its own `totalDuration` as 8 minutes, but `wkc`'s `dependencyTime` shows it waited 10 minutes for `ccs` to be ready, this indicates that `wkc` started polling too early or is not detecting readiness efficiently, and has room to reduce its waiting overhead.

**Example CR status output**

```yaml
status:
  operationTiming:
    - startTime: "2026-07-15T10:00:00Z"
      endTime: "2026-07-15T10:22:30Z"
      totalDuration: "22m30s"
      phase: "Completed"
      dependencyTime:
        - component: analyticsengine
          startTime: "2026-07-15T10:00:00Z"
          readyTime: "2026-07-15T10:08:45Z"
          dependencyDuration: "8m45s"
        - component: ccs
          startTime: "2026-07-15T10:02:00Z"
          readyTime: "2026-07-15T10:10:10Z"
          dependencyDuration: "8m10s"
    - startTime: "2026-07-14T09:00:00Z"
      endTime: "2026-07-14T09:12:00Z"
      totalDuration: "12m0s"
      phase: "Failed"
      dependencyTime:
        - component: analyticsengine
          startTime: "2026-07-14T09:00:00Z"
          readyTime: "2026-07-14T09:08:30Z"
          dependencyDuration: "8m30s"
        - component: ccs
          startTime: "2026-07-14T09:02:00Z"
          readyTime: "2026-07-14T09:10:06Z"
          dependencyDuration: "8m06s"
    - startTime: "2026-07-13T08:00:00Z"
      endTime: "2026-07-13T08:00:45Z"
      totalDuration: "45s"
      phase: "<other status due to unsatisfied pre-check>"
    # ... two more entries (up to five total) ...
```

### 5.5 Guidance to Service Teams

#### What to implement

- Each service operator must add a dedicated `operationTiming` field under `.status`. The field stores the **latest five operation timing entries**, with the **most recent entry as the first item** in the array. Older entries follow in descending order. When a sixth operation completes, the oldest entry is dropped.

- For each operation (install, upgrade, or patch),
1. **Record `startTime`** at the very beginning of the operation, before any dependency checks or work begins.
2. **Record `dependencyTime`** for each immediate dependency: capture `startTime` when the operator begins waiting for that dependency, `readyTime` at the moment it transitions to a ready state, and compute `dependencyDuration` as `readyTime - startTime`. Omit the field entirely if the service has no dependencies.
3. **Record `endTime` and `phase`** when the operation concludes — whether it completes successfully or terminates due to a failure.
4. **Compute `totalDuration`** as `endTime - startTime` and write it as a human-readable string (e.g. `42m30s`).
5. **Maintain a rolling window of the latest five entries.** Prepend the new entry and drop the oldest if the list exceeds five items.
6. **Write the updated list to `.status.operationTiming`** at the end of each operation, using the same status-patching mechanism already used for `progress` and `reconcileHistory`.
7. **Emit Kubernetes Events** at each checkpoint so that `oc describe` surfaces a human-readable timeline without requiring access to the status block:

   | Checkpoint | `reason` | `type` | `message` guidance |
   |---|---|---|---|
   | Operation starts | `OperationStarted` | `Normal` | Include the operation type and version transition, e.g. `"Upgrade operation started: v5.0.0 -> v5.1.0"` |
   | Begins waiting for a dependency / internal CR | `DependencyWaitStarted` | `Normal` | Name the dependency, e.g. `"Waiting for dependency: ccs"` |
   | Dependency / internal CR becomes ready | `DependencyReady` | `Normal` | Name the dependency, e.g. `"Dependency ccs is ready"` |
   | Operation ends | `OperationEnded` | `Normal` (success) or `Warning` (failure) | Set `phase` to `Completed`, `Failed`, `Timeout`, or `Rollback` and include a short outcome description in `message`. All outcomes fold into this single reason — no separate reason per outcome. |

   Example events:

   ```yaml
   # Operation start
   apiVersion: v1
   kind: Event
   metadata:
     generateName: sample-cr.operation-started.
     namespace: cpd-instance
   involvedObject:
     apiVersion: mygroup.ibm.com/v1
     kind: MyService
     name: sample-cr
     namespace: cpd-instance
   reason: OperationStarted
   message: "Upgrade operation started: v5.0.0 -> v5.1.0"
   type: Normal
   firstTimestamp: "2026-07-28T08:10:20Z"
   lastTimestamp: "2026-07-28T08:10:20Z"

   ---
   # Operator begins waiting for a dependency
   apiVersion: v1
   kind: Event
   metadata:
     generateName: sample-cr.dependency-wait-started.
     namespace: cpd-instance
   involvedObject:
     apiVersion: mygroup.ibm.com/v1
     kind: MyService
     name: sample-cr
     namespace: cpd-instance
   reason: DependencyWaitStarted
   message: "Waiting for dependency: ccs"
   type: Normal
   firstTimestamp: "2026-07-28T08:10:25Z"
   lastTimestamp: "2026-07-28T08:10:25Z"

   ---
   # Dependency becomes ready
   apiVersion: v1
   kind: Event
   metadata:
     generateName: sample-cr.dependency-ready.
     namespace: cpd-instance
   involvedObject:
     apiVersion: mygroup.ibm.com/v1
     kind: MyService
     name: sample-cr
     namespace: cpd-instance
   reason: DependencyReady
   message: "Dependency ccs is ready"
   type: Normal
   firstTimestamp: "2026-07-28T08:20:10Z"
   lastTimestamp: "2026-07-28T08:20:10Z"

   ---
   # Operation end (success)
   apiVersion: v1
   kind: Event
   metadata:
     generateName: sample-cr.operation-ended.
     namespace: cpd-instance
   involvedObject:
     apiVersion: mygroup.ibm.com/v1
     kind: MyService
     name: sample-cr
     namespace: cpd-instance
   reason: OperationEnded
   phase: Completed
   message: "Upgrade to v5.1.0 completed successfully"
   type: Normal
   firstTimestamp: "2026-07-28T08:22:47Z"
   lastTimestamp: "2026-07-28T08:22:47Z"
   ```

   For a failed operation, set `type: Warning` and `phase: Failed`:

   ```yaml
   reason: OperationEnded
   phase: Failed
   message: "Upgrade failed: deployment rollout timeout exceeded"
   type: Warning
   firstTimestamp: "2026-07-28T09:05:12Z"
   ```

   Running `oc describe` against the CR will then display a timeline such as:

   ```
   $ oc describe mycr sample-cr -n cpd-instance

   Events:
     Type     Reason                Age    From           Message
     ----     ------                ---    ----           -------
     Normal   OperationStarted      13m    myservice-op   Upgrade operation started: v5.0.0 -> v5.1.0
     Normal   DependencyWaitStarted 13m    myservice-op   Waiting for dependency: ccs
     Normal   DependencyReady       12m    myservice-op   Dependency ccs is ready
     Normal   OperationEnded        now    myservice-op   Upgrade to v5.1.0 completed successfully
   ```

8. **Configure RBAC for Kubernetes Event creation** following the [optional feature RBAC pattern](https://github.ibm.com/PrivateCloud-analytics/CPD-TechSpec/blob/cpd-cli/RBAC_for_Components.md#34-optional-feature-rbac-implementation). The `events` resource permission is **enabled by default** and must be wrapped in a Helm conditional so that it can be **disabled** when `.global.enforceLeastPrivilege` is set to `true`:

   ```yaml
   {{- if not (.Values.global.enforceLeastPrivilege | default false) }}
   - apiGroups: [""]
     resources: ["events"]
     verbs: ["create", "patch", "update"]
   {{- end }}
   ```

   By default, the operator has permission to create events. Setting `.global.enforceLeastPrivilege: true` removes that permission for deployments that require a stricter security posture.

   When the permission is removed, the operator **must skip event emission silently** — all other functionality (status updates, `operationTiming` recording, reconciliation) must continue to work as normal with no regression. Gate every event call on the same flag so that a missing permission never causes the operator to error or stop reconciling:

   **Ansible:**
   ```yaml
   - name: Emit OperationStarted event
     kubernetes.core.k8s:
       # ...event definition...
     when: not (enforce_least_privilege | default false)
   ```

   **Golang:**
   ```go
   if !r.EnforceLeastPrivilege {
       r.Recorder.Event(instance, corev1.EventTypeNormal, "OperationStarted", msg)
   }
   ```

#### Ansible-based operator sample

> **Note**: The following is an illustrative example only. Service teams must adapt the variable names, task structure, and logic to match their own codebase.

```yaml
# At the start of the operation
- name: Record operation start time
  set_fact:
    operation_start_time: "{{ lookup('pipe', 'date -u +%Y-%m-%dT%H:%M:%SZ') }}"

- name: Emit OperationStarted event
  kubernetes.core.k8s:
    state: present
    definition:
      apiVersion: v1
      kind: Event
      metadata:
        generateName: "{{ cr_name }}.operation-started."
        namespace: "{{ namespace }}"
      involvedObject:
        apiVersion: "{{ api_version }}"
        kind: "{{ cr_kind }}"
        name: "{{ cr_name }}"
        namespace: "{{ namespace }}"
      reason: OperationStarted
      message: "Upgrade operation started: {{ current_version }} -> {{ target_version }}"
      type: Normal
      firstTimestamp: "{{ operation_start_time }}"
      lastTimestamp: "{{ operation_start_time }}"

# Before starting to wait for each immediate dependency (repeat per dependency)
- name: Record dependency start time for ws
  set_fact:
    dep_ws_start_time: "{{ lookup('pipe', 'date -u +%Y-%m-%dT%H:%M:%SZ') }}"

- name: Emit DependencyWaitStarted event for ws
  kubernetes.core.k8s:
    state: present
    definition:
      apiVersion: v1
      kind: Event
      metadata:
        generateName: "{{ cr_name }}.dependency-wait-started."
        namespace: "{{ namespace }}"
      involvedObject:
        apiVersion: "{{ api_version }}"
        kind: "{{ cr_kind }}"
        name: "{{ cr_name }}"
        namespace: "{{ namespace }}"
      reason: DependencyWaitStarted
      message: "Waiting for dependency: ws"
      type: Normal
      firstTimestamp: "{{ dep_ws_start_time }}"
      lastTimestamp: "{{ dep_ws_start_time }}"

# After each immediate dependency becomes ready (repeat per dependency)
- name: Record dependency ready time for ws
  set_fact:
    dep_ws_ready_time: "{{ lookup('pipe', 'date -u +%Y-%m-%dT%H:%M:%SZ') }}"

- name: Emit DependencyReady event for ws
  kubernetes.core.k8s:
    state: present
    definition:
      apiVersion: v1
      kind: Event
      metadata:
        generateName: "{{ cr_name }}.dependency-ready."
        namespace: "{{ namespace }}"
      involvedObject:
        apiVersion: "{{ api_version }}"
        kind: "{{ cr_kind }}"
        name: "{{ cr_name }}"
        namespace: "{{ namespace }}"
      reason: DependencyReady
      message: "Dependency ws is ready"
      type: Normal
      firstTimestamp: "{{ dep_ws_ready_time }}"
      lastTimestamp: "{{ dep_ws_ready_time }}"

# At the end of the operation (success or failure)
- name: Record operation end time
  set_fact:
    operation_end_time: "{{ lookup('pipe', 'date -u +%Y-%m-%dT%H:%M:%SZ') }}"

- name: Emit OperationEnded event
  kubernetes.core.k8s:
    state: present
    definition:
      apiVersion: v1
      kind: Event
      metadata:
        generateName: "{{ cr_name }}.operation-ended."
        namespace: "{{ namespace }}"
      involvedObject:
        apiVersion: "{{ api_version }}"
        kind: "{{ cr_kind }}"
        name: "{{ cr_name }}"
        namespace: "{{ namespace }}"
      reason: OperationEnded
      phase: "{{ current_phase }}"
      message: "{{ operation_end_message }}"
      type: "{{ 'Normal' if current_phase == 'Completed' else 'Warning' }}"
      firstTimestamp: "{{ operation_end_time }}"
      lastTimestamp: "{{ operation_end_time }}"

- name: Build new timing entry
  set_fact:
    new_timing_entry:
      startTime: "{{ operation_start_time }}"
      endTime: "{{ operation_end_time }}"
      totalDuration: "{{ ((operation_end_time | to_datetime('%Y-%m-%dT%H:%M:%SZ')) - (operation_start_time | to_datetime('%Y-%m-%dT%H:%M:%SZ'))).seconds }}s"
      phase: "{{ current_phase }}"
      dependencyTime:
        - component: ws
          startTime: "{{ dep_ws_start_time }}"
          readyTime: "{{ dep_ws_ready_time }}"
          dependencyDuration: "{{ ((dep_ws_ready_time | to_datetime('%Y-%m-%dT%H:%M:%SZ')) - (dep_ws_start_time | to_datetime('%Y-%m-%dT%H:%M:%SZ'))).seconds }}s"

- name: Prepend new entry and keep latest 5
  set_fact:
    updated_timing: "{{ ([new_timing_entry | default({})] + (existing_timing | default([])))[:5] }}"

- name: Write operationTiming to CR
  operator_sdk.util.k8s_status:
    api_version: "{{ api_version }}"
    kind: "{{ cr_kind }}"
    name: "{{ cr_name }}"
    namespace: "{{ namespace }}"
    replace_lists: true
    status:
      operationTiming: "{{ updated_timing }}"
```

> **Note**: If the service has no dependencies, omit the `dependencyTime` key from `new_timing_entry` and skip the `DependencyWaitStarted` / `DependencyReady` event tasks entirely.

#### Golang-based operator sample

> **Note**: The following is an illustrative example only. Service teams must adapt the struct types, field names, and logic to match their own codebase and CRD definitions.

```go
// At the start of the operation
operationStartTime := metav1.Now()

// Emit OperationStarted event
r.Recorder.Event(instance, corev1.EventTypeNormal, "OperationStarted",
    fmt.Sprintf("Upgrade operation started: %s -> %s", currentVersion, targetVersion))

// When the operator begins waiting for each immediate dependency
wsStartTime := metav1.Now()

// Emit DependencyWaitStarted event
r.Recorder.Event(instance, corev1.EventTypeNormal, "DependencyWaitStarted",
    "Waiting for dependency: ws")

// After each immediate dependency becomes ready
wsReadyTime := metav1.Now()

// Emit DependencyReady event
r.Recorder.Event(instance, corev1.EventTypeNormal, "DependencyReady",
    "Dependency ws is ready")

// At the end of the operation
operationEndTime := metav1.Now()
duration := operationEndTime.Sub(operationStartTime.Time)

// Emit OperationEnded event — type is Normal on success, Warning on failure
eventType := corev1.EventTypeNormal
if currentPhase != "Completed" {
    eventType = corev1.EventTypeWarning
}
r.Recorder.Event(instance, eventType, "OperationEnded",
    fmt.Sprintf("phase=%s: %s", currentPhase, operationEndMessage))

newEntry := v1beta2.OperationTimingEntry{
    StartTime:     operationStartTime,
    EndTime:       operationEndTime,
    TotalDuration: duration.Round(time.Second).String(),
    Phase:         currentPhase,
    DependencyTime: []v1beta2.DependencyTime{
        {
            Component:          "ws",
            StartTime:          wsStartTime,
            ReadyTime:          wsReadyTime,
            DependencyDuration: wsReadyTime.Sub(wsStartTime.Time).Round(time.Second).String(),
        },
    },
}

// Prepend and keep latest 5
existing := instance.Status.OperationTiming
updated := append([]v1beta2.OperationTimingEntry{newEntry}, existing...)
if len(updated) > 5 {
    updated = updated[:5]
}
instance.Status.OperationTiming = updated

if err := r.Status().Update(ctx, instance); err != nil {
    return ctrl.Result{}, err
}
```

#### Testing checklist

Tests to be done within the service operator:
1. A new timing entry is prepended at the start of each operation.
2. `startTime` is recorded before any dependency checks.
3. `dependencyTime` entries are recorded: `startTime` when the operator begins waiting, `readyTime` when the dependency becomes ready, and `dependencyDuration` computed as the difference.
4. `endTime` and `phase` are recorded correctly for both successful and failed operations.
5. The list never exceeds five entries; the oldest entry is dropped when a sixth is added.
6. Services with no dependencies omit the `dependencyTime` field entirely.
7. An `OperationStarted` event is emitted at the very beginning of the operation.
8. A `DependencyWaitStarted` event is emitted for each dependency before the operator begins waiting, and a `DependencyReady` event is emitted when that dependency becomes ready.
9. An `OperationEnded` event is emitted at the end of the operation with `type: Normal` for `Completed` and `type: Warning` for all other phases (`Failed`, `Timeout`, `Rollback`).
10. Services with no dependencies emit no `DependencyWaitStarted` or `DependencyReady` events.

Tests need to be done using olm-utils
1. Run the `get-cr-status` command to make sure the latest operation `totalDuration` can be displayed.

### 5.6 Implementation Notes by Operator Type

The effort required to add `operationTiming` differs by operator technology:

| Operator type | CRD version bump needed? | Notes |
|---|---|---|
| **Ansible-based** | No | New status fields can be written to the CR without a CRD schema change. No version bump required. |
| **Golang-based** | Yes | CRD changes are bundled with the operator binary. Adding a new status struct requires updating the CRD schema, which requires a CR API version bump (e.g. `v1` → `v1beta2`) and a new operator release. |

### 5.7 Adoption Timeline

Although the implementation effort differs by operator type, `operationTiming` is **mandatory for all services**. Teams should adopt it as early as possible on their next natural release.

| Operator type | Target release |
|---|---|
| Ansible-based operators | **6.0** |
| Golang-based operators | Next natural release that includes a CRD/API version bump |