package tfextras

import (
	"fmt"
	"log"
	"time"

	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/route53"
)

func getSubProvider(d *schema.ResourceData, meta interface{}) interface{} {
	subProvider := d.Get("sub_provider").(string)

	return getClientByName(subProvider)
}

func getTerraformAwsProvider(d *schema.ResourceData, meta interface{}, useSubProvider bool) (interface{}, error) {
	if useSubProvider {
		meta = getSubProvider(d, meta)
	}

	if meta == nil {
		return nil, fmt.Errorf("Cannot get provider, sub_provider is not set or points to not existing provider?")
	}

	return &(meta.(*AWSClient).AWSClient), nil
}

func route53Wrap(orig func(d *schema.ResourceData, meta interface{}) error, useSubProvider bool) func(d *schema.ResourceData, meta interface{}) error {
	return func(d *schema.ResourceData, meta interface{}) (err error) {
		if meta, err = getTerraformAwsProvider(d, meta, useSubProvider); err != nil {
			return
		}

		return orig(d, meta)
	}
}

func resourceAwsRoute53ZoneAssociation(awsprovider *schema.Provider) *schema.Resource {
	res, ok := awsprovider.ResourcesMap["aws_route53_zone_association"]

	if !ok {
		panic("cannot find aws_route53_zone_association in AWS provider")
	}

	res.Schema["sub_provider"] = &schema.Schema{
		Type:     schema.TypeString,
		Optional: true,
		Default:  "",
	}

	// These functions will be called using provider specified in sub_provider directive
	res.Read = route53Wrap(res.Read, true)
	res.Update = route53Wrap(res.Update, true)

	// Keep the original aws provider
	res.Delete = route53Wrap(res.Delete, false)

	// For route53 zones which are associated with different account it is not
	// possible to read the status using account which holds VPC, so the creation
	// must be done from this account, but verification must do the other account,
	// hance another AWS provider must be used.
	res.Create = func(d *schema.ResourceData, meta interface{}) (err error) {
		if err = resourceAwsRoute53ZoneAssociationCreate(d, meta); err != nil {
			return
		}

		// Switch to other provider for verification
		if meta, err = getTerraformAwsProvider(d, meta, true); err != nil {
			return
		}

		return res.Update(d, meta)
	}

	return res
}

// resourceAwsRoute53ZoneAssociationCreate create route53 zone association, but it will
// do verification (read actions) using AWS provider specified in sub_provider directive.
func resourceAwsRoute53ZoneAssociationCreate(d *schema.ResourceData, meta interface{}) error {
	r53 := meta.(*AWSClient).r53conn

	req := &route53.AssociateVPCWithHostedZoneInput{
		HostedZoneId: aws.String(d.Get("zone_id").(string)),
		VPC: &route53.VPC{
			VPCId:     aws.String(d.Get("vpc_id").(string)),
			VPCRegion: aws.String(meta.(*AWSClient).region),
		},
		Comment: aws.String("Managed by Terraform"),
	}
	if w := d.Get("vpc_region"); w != "" {
		req.VPC.VPCRegion = aws.String(w.(string))
	}

	log.Printf("[DEBUG] Associating Route53 Private Zone %s with VPC %s with region %s", *req.HostedZoneId, *req.VPC.VPCId, *req.VPC.VPCRegion)
	var err error
	resp, err := r53.AssociateVPCWithHostedZone(req)
	if err != nil {
		return err
	}

	// Store association id
	d.SetId(fmt.Sprintf("%s:%s", *req.HostedZoneId, *req.VPC.VPCId))
	d.Set("vpc_region", req.VPC.VPCRegion)

	// Wait until we are done initializing
	wait := resource.StateChangeConf{
		Delay:      30 * time.Second,
		Pending:    []string{"PENDING"},
		Target:     []string{"INSYNC"},
		Timeout:    10 * time.Minute,
		MinTimeout: 2 * time.Second,
		Refresh: func() (result interface{}, state string, err error) {
			changeRequest := &route53.GetChangeInput{
				Id: aws.String(cleanChangeID(*resp.ChangeInfo.Id)),
			}
			return resourceAwsGoRoute53Wait(getSubProvider(d, meta).(*AWSClient).r53conn, changeRequest)
		},
	}
	_, err = wait.WaitForState()
	if err != nil {
		return err
	}

	return nil
}
