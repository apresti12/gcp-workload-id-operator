package controllers

import (
	"context"
	"errors"
	"fmt"
	projectxv1 "github.com/MavenWave-DevOps/px-tenant-provisioner/api/v1"
	"github.com/go-logr/logr"
	core "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// TenantBootstrapReconciler reconciles a TenantBootstrap object
type TenantBootstrapReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

type SubjectKind interface {
	ConstructSubject(s projectxv1.Subject, ns string) rbacv1.Subject
	CreateIdentity(ctx context.Context, s projectxv1.Subject, r *TenantBootstrapReconciler, ns string) error
}

type ServiceAccount struct {
	Kind      string
	Name      string
	Namespace string
}

type User struct {
	Kind      string
	Name      string
	Namespace string
}

var l logr.Logger
var tenantConfig projectxv1.TenantBootstrap

//+kubebuilder:rbac:groups=projectx.github.com,resources=tenantbootstraps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=projectx.github.com,resources=tenantbootstraps/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=projectx.github.com,resources=tenantbootstraps/finalizers,verbs=update
//+kubebuilder:rbac:groups=projectx.github.com,resources=*,verbs=*

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the TenantBootstrap object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile

func ConstructRule(rule projectxv1.RbacRule) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{
		Verbs:     rule.Verbs,
		APIGroups: rule.ApiGroups,
		Resources: rule.Resources,
	}
}

func ConstructRole(rc projectxv1.Rbac, ns string) rbacv1.Role {
	var rules []rbacv1.PolicyRule
	for _, v := range rc.Rules {
		rules = append(rules, ConstructRule(v))
	}
	return rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:        rc.RoleName,
			Namespace:   ns,
			Annotations: tenantConfig.GetAnnotations(),
			Labels:      tenantConfig.GetLabels(),
		},
		Rules: rules,
	}
}

func (u *User) ConstructSubject(s projectxv1.Subject, ns string) rbacv1.Subject {
	return rbacv1.Subject{
		Kind:      u.Kind,
		Name:      s.Name,
		Namespace: ns,
	}
}

func (sa *ServiceAccount) ConstructSubject(s projectxv1.Subject, ns string) rbacv1.Subject {
	return rbacv1.Subject{
		Kind:      sa.Kind,
		Name:      s.Name,
		Namespace: ns,
	}
}

func (u *User) CreateIdentity(ctx context.Context, s projectxv1.Subject, r *TenantBootstrapReconciler, ns string) error {
	return nil
}

func (sa *ServiceAccount) CreateIdentity(ctx context.Context, s projectxv1.Subject, r *TenantBootstrapReconciler, ns string) error {
	newSa := &core.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        s.Name,
			Namespace:   ns,
			Annotations: tenantConfig.GetAnnotations(),
			Labels:      tenantConfig.GetLabels(),
		},
	}
	if err := r.Get(ctx, client.ObjectKey{Name: s.Name, Namespace: ns}, newSa); err == nil {
		l.Info("Service Account already exists", "name", s.Name)
		return nil
	} else {
		if err := controllerutil.SetControllerReference(&tenantConfig, newSa, r.Scheme); err != nil {
			l.Error(err, "couldnt set namespace ref")
		}
		if err := r.Create(ctx, newSa); err != nil {
			return err
		}
		return nil
	}
}

func (r *TenantBootstrapReconciler) ConstructRoleBinding(rn string, ns string, subjects []rbacv1.Subject) rbacv1.RoleBinding {
	return rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-rb", rn),
			Namespace:   ns,
			Annotations: tenantConfig.GetAnnotations(),
			Labels:      tenantConfig.GetLabels(),
		},
		Subjects: subjects,
		RoleRef: rbacv1.RoleRef{
			Name: rn,
			Kind: "Role",
		},
	}
}

func (r *TenantBootstrapReconciler) CreateRbac(ctx context.Context, req ctrl.Request, spec projectxv1.TenantBootstrapSpec) error {
	ns := tenantConfig.Namespace
	for _, roleConfig := range spec.Rbac {
		//Create roles

		role := ConstructRole(roleConfig, ns)
		if err := r.Get(ctx, req.NamespacedName, &role); err == nil {
			l.Info("role already exists", "role", role.Name)
		} else {
			if err = controllerutil.SetControllerReference(&tenantConfig, &role, r.Scheme); err != nil {
				l.Error(err, "couldnt set namespace ref")
			}
			if err = r.Create(ctx, &role); err != nil {
				return err
			}
		}
		//Create roleBindings per role
		var subjects []rbacv1.Subject
		for j := 0; j < len(roleConfig.Subjects); j++ {
			var subKind SubjectKind
			switch roleConfig.Subjects[j].Kind {
			case "serviceAccount":
				subKind = &ServiceAccount{
					Kind: "ServiceAccount",
				}
			case "User":
				subKind = &User{
					Kind: "user",
				}
			default:
				subKind = &ServiceAccount{
					Kind: "ServiceAccount",
				}
			}
			subjects = append(subjects, subKind.ConstructSubject(roleConfig.Subjects[j], ns))
			if roleConfig.Subjects[j].Create {
				//Check builtin to create identity method
				if err := subKind.CreateIdentity(ctx, roleConfig.Subjects[j], r, ns); err != nil {
					return err
				}
			}
		}
		rb := r.ConstructRoleBinding(roleConfig.RoleName, ns, subjects)
		if err := r.Get(ctx, req.NamespacedName, &rb); err == nil {
			l.Info("rolebinding already exists", "rolebinding", rb.Name)
		} else {
			if err := controllerutil.SetControllerReference(&tenantConfig, &rb, r.Scheme); err == nil {
				l.Error(err, "couldnt set namespace ref")
			}
			if err := r.Create(ctx, &rb); err != nil {
				l.Error(err, "couldnt create role binding")
			}
		}
	}
	return nil
}

//+kubebuilder:rbac:groups=*,resources=*,verbs=*

func (r *TenantBootstrapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	if err := r.Get(ctx, req.NamespacedName, &tenantConfig); err != nil {
		l.Error(err, "Unable to load config")
		return ctrl.Result{}, nil
	}

	//Implement a namespace check before create
	ns := TenantNamespace{NamespaceSpec: namespaceConfig.Spec}
	if ns.CheckNs(ctx, req, r) != true {
		err := errors.New("namespaces were not created")
		l.Error(err, "try creating namespaces again")
		return ctrl.Result{}, err
	}

	if err := r.CreateRbac(ctx, req, tenantConfig.Spec); err != nil {
		l.Error(err, "could not create rbac")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantBootstrapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&projectxv1.TenantBootstrap{}).
		Owns(&core.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Complete(r)
}
