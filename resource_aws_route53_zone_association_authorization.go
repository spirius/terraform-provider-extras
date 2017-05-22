package tfextras

import (
	"fmt"
	"log"
	"strings"

	"github.com/hashicorp/terraform/helper/schema"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/route53"
)

func resourceAwsRoute53ZoneAssociationAuthorization() *schema.Resource {
	return &schema.Resource{
		Create: resourceAwsRoute53ZoneAssociationAuthorizationCreate,
		Read:   resourceAwsRoute53ZoneAssociationAuthorizationRead,
		Delete: resourceAwsRoute53ZoneAssociationAuthorizationDelete,

		Schema: map[string]*schema.Schema{
			"zone_id": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"vpc_id": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"vpc_region": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},
		},
	}
}

func resourceAwsRoute53ZoneAssociationAuthorizationCreate(d *schema.ResourceData, meta interface{}) error {
	awsclient := meta.(*AWSClient)
	r53 := awsclient.r53conn

	req := &route53.CreateVPCAssociationAuthorizationInput{
		HostedZoneId: aws.String(d.Get("zone_id").(string)),
		VPC: &route53.VPC{
			VPCId:     aws.String(d.Get("vpc_id").(string)),
			VPCRegion: aws.String(awsclient.region),
		},
	}
	if w := d.Get("vpc_region"); w != "" {
		req.VPC.VPCRegion = aws.String(w.(string))
	}

	log.Printf("[DEBUG] Creating Associatiion Authorization Route53 Private Zone %s with VPC %s with region %s", *req.HostedZoneId, *req.VPC.VPCId, *req.VPC.VPCRegion)
	var err error
	_, err = r53.CreateVPCAssociationAuthorization(req)
	if err != nil {
		return err
	}

	// Store association id
	d.SetId(fmt.Sprintf("%s:%s", *req.HostedZoneId, *req.VPC.VPCId))
	d.Set("vpc_region", req.VPC.VPCRegion)

	return resourceAwsRoute53ZoneAssociationAuthorizationRead(d, meta)
}

func resourceAwsRoute53ZoneAssociationAuthorizationRead(d *schema.ResourceData, meta interface{}) error {
	r53 := meta.(*AWSClient).r53conn

	zone_id, vpc_id := resourceAwsRoute53ZoneAssociationAuthorizationParseId(d.Id())
	zone, err := r53.ListVPCAssociationAuthorizations(&route53.ListVPCAssociationAuthorizationsInput{HostedZoneId: aws.String(zone_id)})

	if err != nil {
		// Handle a deleted zone
		if r53err, ok := err.(awserr.Error); ok && r53err.Code() == "NoSuchHostedZone" {
			d.SetId("")
			return nil
		}
		return err
	}

	for _, vpc := range zone.VPCs {
		if vpc_id == *vpc.VPCId {
			// association is there, return
			return nil
		}
	}

	// no association found
	d.SetId("")
	return nil
}

func resourceAwsRoute53ZoneAssociationAuthorizationDelete(d *schema.ResourceData, meta interface{}) error {
	r53 := meta.(*AWSClient).r53conn

	zone_id, vpc_id := resourceAwsRoute53ZoneAssociationAuthorizationParseId(d.Id())
	log.Printf("[DEBUG] Deleting Route53 Association Authorization Private Zone (%s) association (VPC: %s)", zone_id, vpc_id)

	req := &route53.DeleteVPCAssociationAuthorizationInput{
		HostedZoneId: aws.String(zone_id),
		VPC: &route53.VPC{
			VPCId:     aws.String(vpc_id),
			VPCRegion: aws.String(d.Get("vpc_region").(string)),
		},
	}

	_, err := r53.DeleteVPCAssociationAuthorization(req)
	if err != nil {
		return err
	}

	return nil
}

func resourceAwsRoute53ZoneAssociationAuthorizationParseId(id string) (zone_id, vpc_id string) {
	parts := strings.SplitN(id, ":", 2)
	zone_id = parts[0]
	vpc_id = parts[1]
	return
}
