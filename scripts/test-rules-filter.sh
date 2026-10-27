#!/usr/bin/env bash
# test-rules-filter.sh — verify rulesFilter() on a live cluster
#
# A wildcard * in apiGroups/resources of a source Role must be replaced by the
# rules of nss-managed-role-from-<fromNs> (the operator's own permissions in
# <toNs>), with verbs limited to what the source rule allows. The projected Role
# must never grant more than the source rule or the managed Role. A * is kept only
# where the managed Role has a * as well (e.g. the full namespace admin Role created
# by scripts/authorize-namespace.sh).
#
# Usage:
#   ./scripts/test-rules-filter.sh <fromNs> <toNs> [kubectl-context]
#
# Prerequisites:
#   - kubectl available on PATH
#   - Role nss-managed-role-from-<fromNs> must exist in <toNs>. It can be either a
#     restricted Role with explicit rules or the full namespace admin Role.
#   - A NamespaceScope CR must already exist in <fromNs> with <toNs> listed
#     in spec.namespaceMembers, and the operator must be running.
#   - You need patch permission on the NamespaceScope CR and create/delete
#     permission on Roles, RoleBindings, and ServiceAccounts in <fromNs>.

set -euo pipefail

# ── arguments ────────────────────────────────────────────────────────────────
FROM_NS="${1:-}"
TO_NS="${2:-}"
CONTEXT_ARG="${3:-}"

if [[ -z "$FROM_NS" || -z "$TO_NS" ]]; then
    echo "Usage: $0 <fromNs> <toNs> [kubectl-context]" >&2
    exit 1
fi

KCL="kubectl"
if [[ -n "$CONTEXT_ARG" ]]; then
    KCL="kubectl --context $CONTEXT_ARG"
fi

# ── colours ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
pass()  { echo -e "${GREEN}  PASS${NC}  $*"; }
fail()  { echo -e "${RED}  FAIL${NC}  $*"; FAILURES=$((FAILURES+1)); }
info()  { echo -e "${YELLOW}  ----${NC}  $*"; }

FAILURES=0

# ── resource names ────────────────────────────────────────────────────────────
SA="nss-test-sa"

ROLE_BOTH_WILD="nss-test-role-both-wild"   # apiGroups:["*"], resources:["*"]
ROLE_AG_WILD="nss-test-role-ag-wild"       # apiGroups:["*"], resources:["configmaps"]
ROLE_RES_WILD="nss-test-role-res-wild"     # apiGroups:["apps"], resources:["*"]
ROLE_BENIGN="nss-test-role-benign"         # apiGroups:["apps"], resources:["deployments"]
ROLE_ODLM="nss-test-role-odlm"             # apiGroups:["*"], resources:["*"], 7 verbs (ODLM style)
ROLE_MIXED="nss-test-role-mixed"           # apiGroups:["apps","*"], resources:["deployments"]

ALL_ROLES=("$ROLE_BOTH_WILD" "$ROLE_AG_WILD" "$ROLE_RES_WILD" "$ROLE_BENIGN" "$ROLE_ODLM" "$ROLE_MIXED")

# Role holding the operator's own permissions in toNs; wildcards are replaced by its rules.
MANAGED_ROLE="nss-managed-role-from-${FROM_NS}"
if ! $KCL get role "$MANAGED_ROLE" -n "$TO_NS" >/dev/null 2>&1; then
    echo -e "${RED}ERROR${NC}: Role $MANAGED_ROLE not found in namespace $TO_NS." >&2
    echo "       Wildcard rules can only be projected when this Role exists." >&2
    exit 1
fi

# A wildcard in the managed Role (full namespace admin) matches everything, so projected
# Roles may keep a * and there is no resource outside of it.
MANAGED_WILDCARDS=$($KCL get role "$MANAGED_ROLE" -n "$TO_NS" \
    -o jsonpath='{range .rules[*]}{.apiGroups[*]} {.resources[*]}{"\n"}{end}' 2>/dev/null \
    | tr ' ' '\n' | grep -cx '\*' || true)

# A core-group resource the managed Role does not cover, used to check that projected Roles stay within it.
MANAGED_RESOURCES=$($KCL get role "$MANAGED_ROLE" -n "$TO_NS" -o jsonpath='{.rules[*].resources[*]}' 2>/dev/null | tr ' ' '\n')
OUTSIDE_RESOURCE=""
if [[ "$MANAGED_WILDCARDS" == "0" ]]; then
    for candidate in limitranges podtemplates resourcequotas; do
        if ! grep -qx "$candidate" <<<"$MANAGED_RESOURCES"; then
            OUTSIDE_RESOURCE="$candidate"
            break
        fi
    done
fi

# ── find the NamespaceScope CR ───────────────────────────────────────────────
info "Looking for NamespaceScope CR in $FROM_NS …"
NSS_CR=$($KCL get namespacescope -n "$FROM_NS" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z "$NSS_CR" ]]; then
    echo -e "${RED}ERROR${NC}: No NamespaceScope CR found in namespace $FROM_NS." >&2
    echo "       Make sure the CR exists and $TO_NS is listed in spec.namespaceMembers." >&2
    exit 1
fi
info "Found NamespaceScope CR: $NSS_CR"

# ── verify toNs is a member ───────────────────────────────────────────────────
MEMBERS=$($KCL get namespacescope "$NSS_CR" -n "$FROM_NS" \
    -o jsonpath='{.spec.namespaceMembers}' 2>/dev/null || true)
if [[ "$MEMBERS" != *"$TO_NS"* ]]; then
    echo -e "${RED}ERROR${NC}: $TO_NS is not in spec.namespaceMembers of $NSS_CR." >&2
    echo "       Current members: $MEMBERS" >&2
    exit 1
fi

# ── save original serviceAccountMembers ──────────────────────────────────────
ORIG_SA_MEMBERS=$($KCL get namespacescope "$NSS_CR" -n "$FROM_NS" \
    -o jsonpath='{.spec.serviceAccountMembers}' 2>/dev/null || true)

# ── cleanup ───────────────────────────────────────────────────────────────────
cleanup() {
    echo ""
    info "Cleaning up …"

    # restore original serviceAccountMembers (remove test SA)
    if [[ "$ORIG_SA_MEMBERS" == *"$SA"* ]]; then
        # SA was already a member before the test — leave it
        true
    else
        info "Removing $SA from spec.serviceAccountMembers of $NSS_CR …"
        # build the original list without the test SA
        RESTORE_LIST=$(
            $KCL get namespacescope "$NSS_CR" -n "$FROM_NS" \
                -o jsonpath='{.spec.serviceAccountMembers[*]}' 2>/dev/null \
            | tr ' ' '\n' \
            | grep -v "^${SA}$" \
            | awk 'NR==1{printf "%s","\""$0"\""} NR>1{printf ",""\""$0"\""}' \
            || true
        )
        $KCL patch namespacescope "$NSS_CR" -n "$FROM_NS" \
            --type=json \
            -p "[{\"op\":\"replace\",\"path\":\"/spec/serviceAccountMembers\",\"value\":[${RESTORE_LIST}]}]" \
            >/dev/null 2>&1 || true
    fi

    # delete test fixtures from fromNs
    $KCL delete sa "$SA" -n "$FROM_NS" --ignore-not-found >/dev/null 2>&1 || true
    for role in "${ALL_ROLES[@]}"; do
        $KCL delete role "$role" -n "$FROM_NS" --ignore-not-found >/dev/null 2>&1 || true
        $KCL delete rolebinding "${role}-binding" -n "$FROM_NS" --ignore-not-found >/dev/null 2>&1 || true
    done

    # delete any projected roles that landed in toNs
    for role in "${ALL_ROLES[@]}"; do
        local_proj=$(projected_name "$role")
        $KCL delete role "$local_proj" -n "$TO_NS" --ignore-not-found >/dev/null 2>&1 || true
    done

    info "Cleanup done."
}
trap cleanup EXIT

# ── helpers ───────────────────────────────────────────────────────────────────
# Replicates the operator's naming: sha256(roleName+saName+fromNs), first 7 bytes as hex,
# prefixed with the first dot-split segment of roleName.
projected_name() {
    local role="$1"
    local segment hash
    segment=$(echo "$role" | cut -d. -f1)
    hash=$(printf '%s' "${role}${SA}${FROM_NS}" | sha256sum | cut -c1-14)
    echo "${segment}-${hash}"
}

# ── create ServiceAccount ────────────────────────────────────────────────────
echo ""
info "Creating ServiceAccount $SA in $FROM_NS …"
$KCL create sa "$SA" -n "$FROM_NS" --dry-run=client -o yaml | $KCL apply -f - >/dev/null

# ── create Roles + RoleBindings ───────────────────────────────────────────────
info "Creating test Roles and RoleBindings in $FROM_NS …"

# Case 1 — both wildcards, read-only
$KCL apply -f - >/dev/null <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: $ROLE_BOTH_WILD
  namespace: $FROM_NS
rules:
- apiGroups: ["*"]
  resources: ["*"]
  verbs: ["get"]
EOF

# Case 2 — apiGroups wildcard, explicit resources
$KCL apply -f - >/dev/null <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: $ROLE_AG_WILD
  namespace: $FROM_NS
rules:
- apiGroups: ["*"]
  resources: ["configmaps"]
  verbs: ["get"]
EOF

# Case 3 — explicit apiGroups, resources wildcard
$KCL apply -f - >/dev/null <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: $ROLE_RES_WILD
  namespace: $FROM_NS
rules:
- apiGroups: ["apps"]
  resources: ["*"]
  verbs: ["get"]
EOF

# Case 5 — both wildcards with several verbs, as in the ODLM role
$KCL apply -f - >/dev/null <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: $ROLE_ODLM
  namespace: $FROM_NS
rules:
- apiGroups: ["*"]
  resources: ["*"]
  verbs: ["create", "delete", "get", "list", "patch", "update", "watch"]
EOF

# Case 6 — wildcard mixed with an explicit apiGroup
$KCL apply -f - >/dev/null <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: $ROLE_MIXED
  namespace: $FROM_NS
rules:
- apiGroups: ["apps", "*"]
  resources: ["deployments"]
  verbs: ["get"]
EOF

# Case 4 — no wildcards (control)
$KCL apply -f - >/dev/null <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: $ROLE_BENIGN
  namespace: $FROM_NS
rules:
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get"]
EOF

# RoleBindings
for role in "${ALL_ROLES[@]}"; do
$KCL apply -f - >/dev/null <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${role}-binding
  namespace: $FROM_NS
subjects:
- kind: ServiceAccount
  name: $SA
  namespace: $FROM_NS
roleRef:
  kind: Role
  name: $role
  apiGroup: rbac.authorization.k8s.io
EOF
done

# ── add SA to NamespaceScope CR ───────────────────────────────────────────────
info "Adding $SA to spec.serviceAccountMembers of NamespaceScope CR $NSS_CR …"

# Build new members list: existing + test SA (deduplicated)
EXISTING=$($KCL get namespacescope "$NSS_CR" -n "$FROM_NS" \
    -o jsonpath='{.spec.serviceAccountMembers[*]}' 2>/dev/null || true)
NEW_LIST=$(
    { echo "$EXISTING" | tr ' ' '\n'; echo "$SA"; } \
    | sort -u \
    | grep -v '^$' \
    | awk 'NR==1{printf "\""$0"\""} NR>1{printf ",""\""$0"\""}' \
)

$KCL patch namespacescope "$NSS_CR" -n "$FROM_NS" \
    --type=json \
    -p "[{\"op\":\"replace\",\"path\":\"/spec/serviceAccountMembers\",\"value\":[${NEW_LIST}]}]" \
    >/dev/null

# ── wait for reconcile ────────────────────────────────────────────────────────
echo ""
BENIGN_PROJ=$(projected_name "$ROLE_BENIGN")
info "Waiting for operator to project benign role as $BENIGN_PROJ in $TO_NS (up to 60s) …"

deadline=60; elapsed=0
while (( elapsed < deadline )); do
    if $KCL get role "$BENIGN_PROJ" -n "$TO_NS" >/dev/null 2>&1; then
        info "Reconcile complete."
        break
    fi
    sleep 3; (( elapsed+=3 ))
done

if (( elapsed >= deadline )); then
    echo -e "${RED}ERROR${NC}: Timed out waiting for reconcile." >&2
    echo "       Check operator logs and confirm $TO_NS is in spec.namespaceMembers." >&2
    exit 1
fi

sleep 3   # let all roles settle

# ── evaluate results ──────────────────────────────────────────────────────────
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Results  (fromNs=$FROM_NS  toNs=$TO_NS)"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

SA_REF="system:serviceaccount:${FROM_NS}:${SA}"

# Expect: the Role is projected, the wildcard is replaced by the operator's own
# permissions in toNs (nss-managed-role-from-<fromNs>), no wildcard verb is left, a
# wildcard in apiGroups/resources is left only if the managed Role has one, and every
# projected verb is one of the space-separated verbs in $3 (the source verbs).
check_projected_capped() {
    local role="$1" label="$2" allowed="$3"
    local proj
    proj=$(projected_name "$role")

    if ! $KCL get role "$proj" -n "$TO_NS" >/dev/null 2>&1; then
        fail "$label: Role was NOT projected to $TO_NS (is $MANAGED_ROLE present in $TO_NS?)"
        return
    fi

    local rules verbs wildcards v bad=""
    rules=$($KCL get role "$proj" -n "$TO_NS" -o jsonpath='{.rules}' 2>/dev/null)
    verbs=$($KCL get role "$proj" -n "$TO_NS" -o jsonpath='{range .rules[*]}{.verbs[*]}{"\n"}{end}' 2>/dev/null \
        | tr ' ' '\n' | grep -v '^$' | sort -u | tr '\n' ' ')
    if [[ "$MANAGED_WILDCARDS" == "0" ]]; then
        wildcards=$($KCL get role "$proj" -n "$TO_NS" \
            -o jsonpath='{range .rules[*]}{.apiGroups[*]} {.resources[*]} {.verbs[*]}{"\n"}{end}' 2>/dev/null \
            | tr ' ' '\n' | grep -cx '\*' || true)
    else
        wildcards=$($KCL get role "$proj" -n "$TO_NS" \
            -o jsonpath='{range .rules[*]}{.verbs[*]}{"\n"}{end}' 2>/dev/null \
            | tr ' ' '\n' | grep -cx '\*' || true)
    fi

    for v in $verbs; do
        [[ " $allowed " == *" $v "* ]] || bad+="$v "
    done

    if [[ "$wildcards" != "0" ]]; then
        fail "$label: projected Role still contains a wildcard"
        echo "       projected rules: $rules"
    elif [[ -z "$verbs" ]]; then
        fail "$label: projected Role has no verbs"
        echo "       projected rules: $rules"
    elif [[ -n "$bad" ]]; then
        fail "$label: projected Role grants verbs beyond the source rule: $bad— PRIVILEGE ESCALATION"
        echo "       projected rules: $rules"
    else
        pass "$label: wildcard replaced by operator permissions, verbs within: $allowed"
    fi
}

# Expect: the SA is NOT allowed to run <verb> on <resource> in toNs.
# All test Roles are bound to the same SA, so only use this for a verb that none of
# the test Roles grant.
assert_cannot() {
    local label="$1" verb="$2" resource="$3"
    local answer
    answer=$($KCL auth can-i "$verb" "$resource" \
        --as="$SA_REF" -n "$TO_NS" 2>/dev/null || echo "no")
    if [[ "$answer" == "yes" ]]; then
        fail "$label: SA can '$verb $resource' in $TO_NS — PRIVILEGE ESCALATION"
    else
        pass "$label: SA cannot '$verb $resource' in $TO_NS"
    fi
}

# Expect: the explicit part of a mixed list is kept, i.e. the projected Role has a
# rule whose apiGroups contain $3 and whose resources contain $4.
check_projected_has_rule() {
    local role="$1" label="$2" group="$3" resource="$4"
    local proj
    proj=$(projected_name "$role")

    if $KCL get role "$proj" -n "$TO_NS" \
        -o jsonpath='{range .rules[*]}{.apiGroups[*]}|{.resources[*]}{"\n"}{end}' 2>/dev/null \
        | awk -F'|' -v g="$group" -v r="$resource" \
            '{ n=split($1,a," "); m=split($2,b," "); gf=0; rf=0;
               for(i=1;i<=n;i++) if(a[i]==g) gf=1;
               for(i=1;i<=m;i++) if(b[i]==r) rf=1;
               if(gf && rf) found=1 }
             END { exit !found }'; then
        pass "$label: explicit part kept ($group/$resource)"
    else
        fail "$label: no rule for $group/$resource in the projected Role"
    fi
}

# Expect: Role must be projected with rules identical to source
check_projected_faithful() {
    local role="$1" label="$2"
    local proj
    proj=$(projected_name "$role")

    if ! $KCL get role "$proj" -n "$TO_NS" >/dev/null 2>&1; then
        fail "$label: Role was NOT projected to $TO_NS"
        return
    fi

    local src_rules proj_rules
    src_rules=$($KCL get role "$role" -n "$FROM_NS" -o jsonpath='{.rules}' 2>/dev/null)
    proj_rules=$($KCL get role "$proj" -n "$TO_NS" -o jsonpath='{.rules}' 2>/dev/null)

    if [[ "$src_rules" == "$proj_rules" ]]; then
        pass "$label: Rules projected faithfully (identical to source)"
    else
        fail "$label: Projected rules differ from source"
        echo "       source    : $src_rules"
        echo "       projected : $proj_rules"
    fi
}

echo ""
echo "Case 1 — apiGroups:[\"*\"], resources:[\"*\"], verbs:[\"get\"]  → projected from $MANAGED_ROLE, get only"
check_projected_capped "$ROLE_BOTH_WILD" "case1(both-wild)" "get"

echo ""
echo "Case 2 — apiGroups:[\"*\"], resources:[\"configmaps\"], verbs:[\"get\"]  → projected from $MANAGED_ROLE, get only"
check_projected_capped "$ROLE_AG_WILD" "case2(ag-wild)" "get"

echo ""
echo "Case 3 — apiGroups:[\"apps\"], resources:[\"*\"], verbs:[\"get\"]  → projected from $MANAGED_ROLE, get only"
check_projected_capped "$ROLE_RES_WILD" "case3(res-wild)" "get"

echo ""
echo "Case 4 — apiGroups:[\"apps\"], resources:[\"deployments\"], verbs:[\"get\"]  → must project faithfully"
check_projected_faithful "$ROLE_BENIGN" "case4(benign)"

echo ""
echo "Case 5 — apiGroups:[\"*\"], resources:[\"*\"], 7 verbs (ODLM)  → projected from $MANAGED_ROLE, never deletecollection"
check_projected_capped "$ROLE_ODLM" "case5(odlm)" "create delete get list patch update watch"
assert_cannot "case5(odlm)" deletecollection pods

echo ""
echo "Case 6 — apiGroups:[\"apps\",\"*\"], resources:[\"deployments\"], verbs:[\"get\"]  → apps part kept, wildcard part from $MANAGED_ROLE, get only"
check_projected_capped "$ROLE_MIXED" "case6(mixed)" "get"
check_projected_has_rule "$ROLE_MIXED" "case6(mixed)" "apps" "deployments"

echo ""
echo "Case 7 — projected Roles never exceed $MANAGED_ROLE"
if [[ -n "$OUTSIDE_RESOURCE" ]]; then
    # All test Roles are bound to the same SA and none of them grants this resource,
    # and $MANAGED_ROLE does not cover it, so the SA must not be able to read it.
    assert_cannot "case7(within-managed)" get "$OUTSIDE_RESOURCE"
else
    info "case7(within-managed): skipped, $MANAGED_ROLE covers all candidate resources"
fi

# ── summary ───────────────────────────────────────────────────────────────────
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
if (( FAILURES == 0 )); then
    echo -e "${GREEN}  ALL CASES PASSED${NC}"
else
    echo -e "${RED}  $FAILURES CASE(S) FAILED${NC}"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

exit $FAILURES
