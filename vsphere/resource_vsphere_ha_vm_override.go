package vsphere

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
	"github.com/terraform-providers/terraform-provider-vsphere/vsphere/internal/helper/clustercomputeresource"
	"github.com/terraform-providers/terraform-provider-vsphere/vsphere/internal/helper/structure"
	"github.com/terraform-providers/terraform-provider-vsphere/vsphere/internal/helper/viapi"
	"github.com/terraform-providers/terraform-provider-vsphere/vsphere/internal/helper/virtualmachine"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"
)

const resourceVSphereHaVMOverrideName = "vsphere_ha_vm_override"

func resourceVSphereHaVMOverride() *schema.Resource {
	return &schema.Resource{
		Create: resourceVSphereHaVMOverrideCreate,
		Read:   resourceVSphereHaVMOverrideRead,
		Update: resourceVSphereHaVMOverrideUpdate,
		Delete: resourceVSphereHaVMOverrideDelete,
		Importer: &schema.ResourceImporter{
			State: resourceVSphereHaVMOverrideImport,
		},

		Schema: map[string]*schema.Schema{
			"compute_cluster_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The managed object ID of the cluster.",
			},
			"virtual_machine_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The managed object ID of the virtual machine.",
			},
			"drs_enabled": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "Enable DRS for this virtual machine.",
			},
			"drs_automation_level": {
				Type:         schema.TypeString,
				Optional:     true,
				Default:      string(types.DrsBehaviorManual),
				Description:  "The automation level for this virtual machine in the cluster. Can be one of manual, partiallyAutomated, or fullyAutomated.",
				ValidateFunc: validation.StringInSlice(drsBehaviorAllowedValues, false),
			},
		},
	}
}

func resourceVSphereHaVMOverrideCreate(d *schema.ResourceData, meta interface{}) error {
	log.Printf("[DEBUG] %s: Beginning create", resourceVSphereHaVMOverrideIDString(d))

	cluster, vm, err := resourceVSphereHaVMOverrideObjects(d, meta)
	if err != nil {
		return err
	}

	info, err := expandClusterDrsVMConfigInfo(d, vm)
	if err != nil {
		return err
	}
	spec := &types.ClusterConfigSpecEx{
		DrsVmConfigSpec: []types.ClusterDrsVmConfigSpec{
			{
				ArrayUpdateSpec: types.ArrayUpdateSpec{
					Operation: types.ArrayUpdateOperationAdd,
				},
				Info: info,
			},
		},
	}

	if err = clustercomputeresource.Reconfigure(cluster, spec); err != nil {
		return err
	}

	id, err := resourceVSphereHaVMOverrideFlattenID(cluster, vm)
	if err != nil {
		return fmt.Errorf("cannot compute ID of created resource: %s", err)
	}
	d.SetId(id)

	log.Printf("[DEBUG] %s: Create finished successfully", resourceVSphereHaVMOverrideIDString(d))
	return resourceVSphereHaVMOverrideRead(d, meta)
}

func resourceVSphereHaVMOverrideRead(d *schema.ResourceData, meta interface{}) error {
	log.Printf("[DEBUG] %s: Beginning read", resourceVSphereHaVMOverrideIDString(d))

	cluster, vm, err := resourceVSphereHaVMOverrideObjects(d, meta)
	if err != nil {
		return err
	}

	info, err := resourceVSphereHaVMOverrideFindEntry(cluster, vm)
	if err != nil {
		return err
	}

	if info == nil {
		// The configuration is missing, blank out the ID so it can be re-created.
		d.SetId("")
		return nil
	}

	// Save the compute_cluster_id and virtual_machine_id here. These are
	// ForceNew, but we set these for completeness on import so that if the wrong
	// cluster/VM combo was used, it will be noted.
	if err = d.Set("compute_cluster_id", cluster.Reference().Value); err != nil {
		return fmt.Errorf("error setting attribute \"compute_cluster_id\": %s", err)
	}

	props, err := virtualmachine.Properties(vm)
	if err != nil {
		return fmt.Errorf("error getting properties of virtual machine: %s", err)
	}
	if err = d.Set("virtual_machine_id", props.Config.Uuid); err != nil {
		return fmt.Errorf("error setting attribute \"virtual_machine_id\": %s", err)
	}

	if err = flattenClusterDrsVMConfigInfo(d, info); err != nil {
		return err
	}

	log.Printf("[DEBUG] %s: Read completed successfully", resourceVSphereHaVMOverrideIDString(d))
	return nil
}

func resourceVSphereHaVMOverrideUpdate(d *schema.ResourceData, meta interface{}) error {
	log.Printf("[DEBUG] %s: Beginning update", resourceVSphereHaVMOverrideIDString(d))

	cluster, vm, err := resourceVSphereHaVMOverrideObjects(d, meta)
	if err != nil {
		return err
	}

	info, err := expandClusterDrsVMConfigInfo(d, vm)
	if err != nil {
		return err
	}
	spec := &types.ClusterConfigSpecEx{
		DrsVmConfigSpec: []types.ClusterDrsVmConfigSpec{
			{
				ArrayUpdateSpec: types.ArrayUpdateSpec{
					// NOTE: ArrayUpdateOperationAdd here replaces existing entries,
					// versus adding duplicates or "merging" old settings with new ones
					// that have missing fields.
					Operation: types.ArrayUpdateOperationAdd,
				},
				Info: info,
			},
		},
	}

	if err := clustercomputeresource.Reconfigure(cluster, spec); err != nil {
		return err
	}

	log.Printf("[DEBUG] %s: Update finished successfully", resourceVSphereHaVMOverrideIDString(d))
	return resourceVSphereHaVMOverrideRead(d, meta)
}

func resourceVSphereHaVMOverrideDelete(d *schema.ResourceData, meta interface{}) error {
	log.Printf("[DEBUG] %s: Beginning delete", resourceVSphereHaVMOverrideIDString(d))

	cluster, vm, err := resourceVSphereHaVMOverrideObjects(d, meta)
	if err != nil {
		return err
	}

	spec := &types.ClusterConfigSpecEx{
		DrsVmConfigSpec: []types.ClusterDrsVmConfigSpec{
			{
				ArrayUpdateSpec: types.ArrayUpdateSpec{
					Operation: types.ArrayUpdateOperationRemove,
					RemoveKey: vm.Reference(),
				},
			},
		},
	}

	if err := clustercomputeresource.Reconfigure(cluster, spec); err != nil {
		return err
	}

	log.Printf("[DEBUG] %s: Deleted successfully", resourceVSphereHaVMOverrideIDString(d))
	return nil
}

func resourceVSphereHaVMOverrideImport(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	var data map[string]string
	if err := json.Unmarshal([]byte(d.Id()), &data); err != nil {
		return nil, err
	}
	clusterPath, ok := data["compute_cluster_path"]
	if !ok {
		return nil, errors.New("missing compute_cluster_path in input data")
	}
	vmPath, ok := data["virtual_machine_path"]
	if !ok {
		return nil, errors.New("missing virtual_machine_path in input data")
	}

	client, err := resourceVSphereHaVMOverrideClient(meta)
	if err != nil {
		return nil, err
	}

	cluster, err := clustercomputeresource.FromPath(client, clusterPath, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot locate cluster %q: %s", clusterPath, err)
	}

	vm, err := virtualmachine.FromPath(client, vmPath, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot locate virtual machine %q: %s", vmPath, err)
	}

	id, err := resourceVSphereHaVMOverrideFlattenID(cluster, vm)
	if err != nil {
		return nil, fmt.Errorf("cannot compute ID of imported resource: %s", err)
	}
	d.SetId(id)
	return []*schema.ResourceData{d}, nil
}

// resourceVSphereHaVMOverrideIDString prints a friendly string for the
// vsphere_storage_drs_vm_config resource.
func resourceVSphereHaVMOverrideIDString(d structure.ResourceIDStringer) string {
	return structure.ResourceIDString(d, resourceVSphereHaVMOverrideName)
}

// resourceVSphereHaVMOverrideFlattenID makes an ID for the
// vsphere_storage_drs_vm_config resource.
func resourceVSphereHaVMOverrideFlattenID(cluster *object.ClusterComputeResource, vm *object.VirtualMachine) (string, error) {
	clusterID := cluster.Reference().Value
	props, err := virtualmachine.Properties(vm)
	if err != nil {
		return "", fmt.Errorf("cannot compute ID off of properties of virtual machine: %s", err)
	}
	vmID := props.Config.Uuid
	return strings.Join([]string{clusterID, vmID}, ":"), nil
}

// resourceVSphereHaVMOverrideParseID parses an ID for the
// vsphere_storage_drs_vm_config and outputs its parts.
func resourceVSphereHaVMOverrideParseID(id string) (string, string, error) {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("bad ID %q", id)
	}
	return parts[0], parts[1], nil
}

// resourceVSphereHaVMOverrideFindEntry attempts to locate an existing VM
// config in a Storage Pod's DRS configuration. It's used by the resource's
// read functionality and tests. nil is returned if the entry cannot be found.
func resourceVSphereHaVMOverrideFindEntry(
	cluster *object.ClusterComputeResource,
	vm *object.VirtualMachine,
) (*types.ClusterDrsVmConfigInfo, error) {
	props, err := clustercomputeresource.Properties(cluster)
	if err != nil {
		return nil, fmt.Errorf("error fetching cluster properties: %s", err)
	}

	for _, info := range props.ConfigurationEx.(*types.ClusterConfigInfoEx).DrsVmConfig {
		if info.Key == vm.Reference() {
			log.Printf("[DEBUG] Found DRS config info for VM %q in cluster %q", vm.Name(), cluster.Name())
			return &info, nil
		}
	}

	log.Printf("[DEBUG] No DRS config info found for VM %q in cluster %q", vm.Name(), cluster.Name())
	return nil, nil
}

// resourceVSphereHaVMOverrideObjects handles the fetching of the cluster and
// virtual machine depending on what attributes are available:
// * If the resource ID is available, the data is derived from the ID.
// * If not, it's derived from the compute_cluster_id and virtual_machine_id
// attributes.
func resourceVSphereHaVMOverrideObjects(
	d *schema.ResourceData,
	meta interface{},
) (*object.ClusterComputeResource, *object.VirtualMachine, error) {
	if d.Id() != "" {
		return resourceVSphereHaVMOverrideObjectsFromID(d, meta)
	}
	return resourceVSphereHaVMOverrideObjectsFromAttributes(d, meta)
}

func resourceVSphereHaVMOverrideObjectsFromAttributes(
	d *schema.ResourceData,
	meta interface{},
) (*object.ClusterComputeResource, *object.VirtualMachine, error) {
	return resourceVSphereHaVMOverrideFetchObjects(
		meta,
		d.Get("compute_cluster_id").(string),
		d.Get("virtual_machine_id").(string),
	)
}

func resourceVSphereHaVMOverrideObjectsFromID(
	d structure.ResourceIDStringer,
	meta interface{},
) (*object.ClusterComputeResource, *object.VirtualMachine, error) {
	// Note that this function uses structure.ResourceIDStringer to satisfy
	// interfacer. Adding exceptions in the comments does not seem to work.
	// Change this back to ResourceData if it's needed in the future.
	clusterID, vmID, err := resourceVSphereHaVMOverrideParseID(d.Id())
	if err != nil {
		return nil, nil, err
	}

	return resourceVSphereHaVMOverrideFetchObjects(meta, clusterID, vmID)
}

func resourceVSphereHaVMOverrideFetchObjects(
	meta interface{},
	clusterID string,
	vmID string,
) (*object.ClusterComputeResource, *object.VirtualMachine, error) {
	client, err := resourceVSphereHaVMOverrideClient(meta)
	if err != nil {
		return nil, nil, err
	}

	cluster, err := clustercomputeresource.FromID(client, clusterID)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot locate cluster: %s", err)
	}

	vm, err := virtualmachine.FromUUID(client, vmID)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot locate virtual machine: %s", err)
	}

	return cluster, vm, nil
}

func resourceVSphereHaVMOverrideClient(meta interface{}) (*govmomi.Client, error) {
	client := meta.(*VSphereClient).vimClient
	if err := viapi.ValidateVirtualCenter(client); err != nil {
		return nil, err
	}
	return client, nil
}