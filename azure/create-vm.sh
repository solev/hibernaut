#!/usr/bin/env bash
# Creates the Hibernaut host on Azure: a VM with nested virtualization (needed for Firecracker),
# a nightly auto-shutdown, and a managed identity that can only read and deallocate the VM.
# Then hardens SSH and installs the idle auto-deallocation timer on it.
set -euo pipefail
source "$(dirname "$0")/common.sh"

ROLE_NAME="VM Self Deallocate"

echo "==> Creating resource group $RESOURCE_GROUP in $LOCATION"
az group create -n "$RESOURCE_GROUP" -l "$LOCATION" -o none

echo "==> Creating VM $VM_NAME ($VM_SIZE)"
az vm create -g "$RESOURCE_GROUP" -n "$VM_NAME" -l "$LOCATION" \
  --image Ubuntu2404 --size "$VM_SIZE" \
  --os-disk-size-gb 128 --storage-sku StandardSSD_LRS \
  --admin-username "$VM_USER" --ssh-key-values "$SSH_PUBLIC_KEY" \
  --nsg-rule SSH --public-ip-sku Standard \
  --assign-identity '[system]' -o none

echo "==> Scheduling nightly deallocation at $SHUTDOWN_UTC UTC"
az vm auto-shutdown -g "$RESOURCE_GROUP" -n "$VM_NAME" --time "$SHUTDOWN_UTC" -o none

echo "==> Granting the VM permission to deallocate itself"
subscription_id=$(az account show --query id -o tsv)
if [[ -z "$(az role definition list --custom-role-only --name "$ROLE_NAME" --query '[].roleName' -o tsv)" ]]; then
  role_file=$(mktemp)
  trap 'rm -f "$role_file"' EXIT
  cat > "$role_file" <<EOF
{
  "Name": "$ROLE_NAME",
  "Description": "Lets a VM identity read and deallocate a VM.",
  "Actions": ["Microsoft.Compute/virtualMachines/read", "Microsoft.Compute/virtualMachines/deallocate/action"],
  "AssignableScopes": ["/subscriptions/$subscription_id"]
}
EOF
  az role definition create --role-definition "@$role_file" -o none
fi

vm_id=$(az vm show -g "$RESOURCE_GROUP" -n "$VM_NAME" --query id -o tsv)
principal_id=$(az vm show -g "$RESOURCE_GROUP" -n "$VM_NAME" --query identity.principalId -o tsv)
# A freshly created custom role can take a minute to become assignable.
for attempt in $(seq 1 12); do
  if az role assignment create --assignee-object-id "$principal_id" --assignee-principal-type ServicePrincipal \
    --role "$ROLE_NAME" --scope "$vm_id" -o none 2>/dev/null; then
    break
  fi
  (( attempt == 12 )) && { echo "Role assignment failed" >&2; exit 1; }
  sleep 15
done

ip=$(vm_ip)
echo "==> Configuring host at $ip"
wait_for_ssh "$ip"
sync_to_vm "$ip"
ssh "$VM_USER@$ip" 'hibernaut/vm/setup-host.sh'

echo
echo "Done. Next: ./azure/up.sh installs and starts E2B on the VM."
