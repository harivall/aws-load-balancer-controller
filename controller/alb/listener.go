package alb

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/coreos/alb-ingress-controller/awsutil"
	"github.com/coreos/alb-ingress-controller/controller/config"
	"github.com/coreos/alb-ingress-controller/log"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
)

// Listener contains the relevant ID, Rules, and current/desired Listeners
type Listener struct {
	IngressID       *string
	CurrentListener *elbv2.Listener
	DesiredListener *elbv2.Listener
	Rules           Rules
	deleted         bool
}

// NewListener returns a new alb.Listener based on the parameters provided.
func NewListener(annotations *config.AnnotationsT, ingressID *string) []*Listener {
	listeners := []*Listener{}

	for _, port := range annotations.Port {

		listener := &elbv2.Listener{
			Port:     aws.Int64(80),
			Protocol: aws.String("HTTP"),
			DefaultActions: []*elbv2.Action{
				{
					Type: aws.String("forward"),
				},
			},
		}

		if annotations.CertificateArn != nil {
			listener.Certificates = []*elbv2.Certificate{
				{CertificateArn: annotations.CertificateArn},
			}
			listener.Protocol = aws.String("HTTPS")
			listener.Port = aws.Int64(443)
		}

		if annotations.Port != nil {
			listener.Port = port
		}

		listenerT := &Listener{
			DesiredListener: listener,
			IngressID:       ingressID,
		}

		listeners = append(listeners, listenerT)
	}

	return listeners
}

// SyncState compares the current and desired state of this Listener instance. Comparison
// results in no action, the creation, the deletion, or the modification of an AWS listener to
// satisfy the ingress's current state.
func (l *Listener) SyncState(lb *LoadBalancer) *Listener {

	switch {
	// No DesiredState means Listener should be deleted.
	case l.DesiredListener == nil:
		log.Infof("Start Listener deletion.", *l.IngressID)
		l.delete(lb)

	// No CurrentState means Listener doesn't exist in AWS and should be created.
	case l.CurrentListener == nil:
		log.Infof("Start Listener creation.", *l.IngressID)
		l.create(lb)

	// Current and Desired exist and need for modification should be evaluated.
	case l.needsModification(l.DesiredListener):
		log.Infof("Start Listener modification.", *l.IngressID)
		l.modify(lb)

	default:
		log.Debugf("No listener modification required.", *l.IngressID)
	}

	return l
}

// Adds a Listener to an existing ALB in AWS. This Listener maps the ALB to an existing TargetGroup.
func (l *Listener) create(lb *LoadBalancer) error {
	l.DesiredListener.LoadBalancerArn = lb.CurrentLoadBalancer.LoadBalancerArn

	// TODO: If we couldn't resolve default, we 'default' to the first targetgroup known.
	// Questionable approach.
	l.DesiredListener.DefaultActions[0].TargetGroupArn = lb.TargetGroups[0].CurrentTargetGroup.TargetGroupArn

	// Look for the default rule in the list of rules known to the Listener. If the default is found,
	// use the Kubernetes service name attached to that.
	for _, rule := range l.Rules {
		if *rule.DesiredRule.IsDefault {
			log.Infof("Located default rule. Rule: %s", *l.IngressID, log.Prettify(rule.DesiredRule))
			tgIndex := lb.TargetGroups.LookupBySvc(rule.SvcName)
			if tgIndex < 0 {
				log.Errorf("Failed to locate TargetGroup related to this service. Defaulting to first Target Group. SVC: %s",
					*l.IngressID, rule.SvcName)
			} else {
				ctg := lb.TargetGroups[tgIndex].CurrentTargetGroup
				l.DesiredListener.DefaultActions[0].TargetGroupArn = ctg.TargetGroupArn
			}
		}
	}

	createListenerInput := &elbv2.CreateListenerInput{
		Certificates:    l.DesiredListener.Certificates,
		LoadBalancerArn: l.DesiredListener.LoadBalancerArn,
		Protocol:        l.DesiredListener.Protocol,
		Port:            l.DesiredListener.Port,
		DefaultActions: []*elbv2.Action{
			{
				Type:           l.DesiredListener.DefaultActions[0].Type,
				TargetGroupArn: l.DesiredListener.DefaultActions[0].TargetGroupArn,
			},
		},
	}

	createListenerOutput, err := awsutil.Elbv2svc.Svc.CreateListener(createListenerInput)
	if err != nil && err.(awserr.Error).Code() != "TargetGroupAssociationLimit" {
		awsutil.AWSErrorCount.With(prometheus.Labels{"service": "ELBV2", "request": "CreateListener"}).Add(float64(1))
		log.Errorf("Failed Listener creation. Error: %s.", *l.IngressID, err.Error())
		return err
	} else if err != nil && err.(awserr.Error).Code() == "TargetGroupAssociationLimit" {
		awsutil.AWSErrorCount.With(prometheus.Labels{"service": "ELBV2", "request": "CreateListener"}).Add(float64(1))
		glog.Error("Received a TargetGroupAssociationLimit error")
		// Something strange happening here, the original Listener doesnt have the LoadBalancerArn but a describe will return a Listener with the ARN
		// l, _ := elbv2svc.describeListeners(lb.LoadBalancer.LoadBalancerArn)
		log.Errorf("Failed Listener creation. Error: %s.", *l.IngressID, err.Error())
		return err
	}

	l.CurrentListener = createListenerOutput.Listeners[0]
	log.Infof("Completed Listener creation. ARN: %s | Port: %s | Proto: %s.",
		*l.IngressID, *l.CurrentListener.ListenerArn, *l.CurrentListener.Port, *l.CurrentListener.Protocol)
	return nil
}

// Modifies a listener
func (l *Listener) modify(lb *LoadBalancer) error {
	if l.CurrentListener == nil {
		// not a modify, a create
		return l.create(lb)
	}

	glog.Infof("Modifying existing %s listener %s", *lb.ID, *l.CurrentListener.ListenerArn)
	//glog.Infof("Have %v, want %v", l.CurrentListener, l.DesiredListener)
	glog.Info("NOT IMPLEMENTED!!!!")

	log.Infof("Completed Listener modification. ARN: %s | Port: %s | Proto: %s.",
		*l.IngressID, *l.CurrentListener.ListenerArn, *l.CurrentListener.Port, *l.CurrentListener.Protocol)
	return nil
}

// Deletes a Listener from an existing ALB in AWS.
func (l *Listener) delete(lb *LoadBalancer) error {
	deleteListenerInput := &elbv2.DeleteListenerInput{
		ListenerArn: l.CurrentListener.ListenerArn,
	}

	// Debug logger to introspect DeleteListener request
	glog.Infof("Delete listener %s", *l.CurrentListener.ListenerArn)
	_, err := awsutil.Elbv2svc.Svc.DeleteListener(deleteListenerInput)
	if err != nil {
		// When listener is not found, there is no work to be done, so return nil.
		awsErr := err.(awserr.Error)
		if awsErr.Code() == elbv2.ErrCodeListenerNotFoundException {
			// TODO: Reorder syncs so route53 is last and this is handled in R53 resource record set syncs
			// (relates to https://git.tm.tmcs/kubernetes/alb-ingress/issues/33)
			log.Warnf("Listener not found during deletion attempt. It was likely already deleted when the ELBV2 (ALB) was deleted.", *l.IngressID)
			log.Infof("Completed Listener deletion. ARN: %s", *l.IngressID, *l.CurrentListener.ListenerArn)
			lb.Deleted = true
			return nil
		}

		awsutil.AWSErrorCount.With(prometheus.Labels{"service": "ELBV2", "request": "DeleteListener"}).Add(float64(1))
		return err
	}

	l.deleted = true
	log.Infof("Completed Listener deletion. ARN: %s", *l.IngressID, *l.CurrentListener.ListenerArn)
	return nil
}

func (l *Listener) needsModification(target *elbv2.Listener) bool {
	switch {
	case l.CurrentListener == nil:
		return true
	case !awsutil.DeepEqual(l.CurrentListener.Port, target.Port):
		return true
	case !awsutil.DeepEqual(l.CurrentListener.Protocol, target.Protocol):
		return true
	case !awsutil.DeepEqual(l.CurrentListener.Certificates, target.Certificates):
		return true
	}
	return false
}
