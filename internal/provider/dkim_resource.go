package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &DKIMResource{}

type DKIMResource struct{ client *Client }

// dnsRecordObjectType is the schema type for a single DNS record object in state.
var dnsRecordObjectType = types.ObjectType{
	AttrTypes: map[string]attr.Type{
		"type":    types.StringType,
		"name":    types.StringType,
		"content": types.StringType,
	},
}

type DKIMResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Domain     types.String `tfsdk:"domain"`
	Algorithm  types.String `tfsdk:"algorithm"`
	Selector   types.String `tfsdk:"selector"`
	DNSRecords types.List   `tfsdk:"dns_records"`
}

func NewDKIMResource() resource.Resource { return &DKIMResource{} }

func (r *DKIMResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dkim"
}

func (r *DKIMResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Generates a DKIM signing key for a domain. Changing domain, algorithm, or selector forces recreation.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resource identifier in the form <domain>/<selector>.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"domain": schema.StringAttribute{
				Required:    true,
				Description: "Domain to generate a DKIM key for.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"algorithm": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Signing algorithm: Ed25519 (default) or RSA-SHA-256.",
				Default:     stringdefault.StaticString("Ed25519"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"selector": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "DKIM selector name (default: stalwart).",
				Default:     stringdefault.StaticString("stalwart"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"dns_records": schema.ListNestedAttribute{
				Computed:    true,
				Description: "DNS records to publish for this DKIM key (TXT, MX, SPF, etc.).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type":    schema.StringAttribute{Computed: true},
						"name":    schema.StringAttribute{Computed: true},
						"content": schema.StringAttribute{Computed: true},
					},
				},
			},
		},
	}
}

func (r *DKIMResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *DKIMResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DKIMResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	selector := plan.Selector.ValueString()
	records, err := r.client.CreateDKIM(ctx, DKIMRequest{
		Domain:    plan.Domain.ValueString(),
		Algorithm: plan.Algorithm.ValueString(),
		Selector:  &selector,
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to create DKIM key", err.Error())
		return
	}

	plan.ID = types.StringValue(plan.Domain.ValueString() + "/" + selector)
	plan.DNSRecords = dnsRecordsToList(records)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes dns_records from the API; if the domain is gone, removes resource.
func (r *DKIMResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DKIMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	records, err := r.client.GetDNSRecords(ctx, state.Domain.ValueString())
	if err != nil {
		var nf ErrNotFound
		if errors.As(err, &nf) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read DKIM DNS records", err.Error())
		return
	}

	state.DNSRecords = dnsRecordsToList(records)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called because all attributes have RequiresReplace.
func (r *DKIMResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
}

// Delete removes the DKIM key by clearing its settings prefix.
func (r *DKIMResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DKIMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// DKIM keys are stored under signature.<selector>. in Stalwart settings.
	prefix := "signature." + state.Selector.ValueString() + "."
	if err := r.client.ClearSettings(ctx, prefix); err != nil {
		resp.Diagnostics.AddError("Failed to delete DKIM key", err.Error())
	}
}

func dnsRecordsToList(records []DNSRecord) types.List {
	vals := make([]attr.Value, len(records))
	for i, r := range records {
		vals[i] = types.ObjectValueMust(
			dnsRecordObjectType.AttrTypes,
			map[string]attr.Value{
				"type":    types.StringValue(r.Type),
				"name":    types.StringValue(r.Name),
				"content": types.StringValue(r.Content),
			},
		)
	}
	return types.ListValueMust(dnsRecordObjectType, vals)
}
