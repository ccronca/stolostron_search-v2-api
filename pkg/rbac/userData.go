// Copyright Contributors to the Open Cluster Management project
package rbac

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/stolostron/search-v2-api/pkg/config"
	authz "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// Contains data about the resources the user is allowed to access.
type userData struct {
	managedClusters map[string]string     // Managed clusters where the user has view access.
	csResources     []resource            // Cluster-scoped resources on hub the user has list access.
	nsResources     map[string][]resource // Namespaced resources on hub the user has list access.

	// Internal fields to manage the cache.
	clustersErr       error      // Error while updating clusters data.
	clustersLock      sync.Mutex // Locks when clusters data is being updated.
	clustersUpdatedAt time.Time  // Time clusters was last updated.

	csrErr       error      // Error while updating cluster-scoped resources data.
	csrLock      sync.Mutex // Locks when cluster-scoped resources data is being updated.
	csrUpdatedAt time.Time  // Time cluster-scoped resources was last updated.

	nsrErr       error      // Error while updating namespaced resources data.
	nsrLock      sync.Mutex // Locks when namespaced resources data is being updated.
	nsrUpdatedAt time.Time  // Time namespaced resources was last updated.

	authzClient v1.AuthorizationV1Interface
}

func (cache *Cache) GetUserData(ctx context.Context, clientToken string,
	authzClient v1.AuthorizationV1Interface) (*userData, error) {
	var user *userData
	uid := cache.tokenReviews[clientToken].tokenReview.Status.User.UID //get uid from tokenreview
	cache.usersLock.Lock()
	defer cache.usersLock.Unlock()
	cachedUserData, userDataExists := cache.users[uid] //check if userData cache for user already exists
	// UserDataExists and its valid
	if userDataExists && userCacheValid(cachedUserData) {
		klog.V(5).Info("Using user data from cache.")
		return cachedUserData, nil
	} else {
		// User not in cache , Initialize and assign to the UID
		user = &userData{}
		cache.users[uid] = user
		// We want to setup the client if passed, this is only for unit tests
		if authzClient != nil {
			user.authzClient = authzClient
		}
	}
	userData, err := user.getNamespacedResources(cache, ctx, clientToken)

	// Get cluster scoped resources for the user
	// TO DO : Make this parallel operation
	if err == nil {
		klog.V(5).Info("No errors on namespacedresources present for: ",
			cache.tokenReviews[clientToken].tokenReview.Status.User.Username)
		userData, err = user.getClusterScopedResources(cache, ctx, clientToken)
	}
	return userData, err

}

/* Cache is Valid if the csrUpdatedAt and nsrUpdatedAt times are before the
Cache expiry time */
func userCacheValid(user *userData) bool {
	if (time.Now().Before(user.csrUpdatedAt.Add(time.Duration(config.Cfg.UserCacheTTL) * time.Millisecond))) &&
		(time.Now().Before(user.nsrUpdatedAt.Add(time.Duration(config.Cfg.UserCacheTTL) * time.Millisecond))) {
		return true
	}
	return false
}

// The following achieves same result as oc auth can-i list <resource> --as=<user>
func (user *userData) getClusterScopedResources(cache *Cache, ctx context.Context,
	clientToken string) (*userData, error) {

	// get all cluster scoped from shared cache:
	klog.V(5).Info("Getting cluster scoped resources from shared cache.")
	user.csrErr = nil
	user.csrLock.Lock()
	defer user.csrLock.Unlock()

	// Not present in cache, find all cluster scoped resources
	clusterScopedResources := cache.shared.csResources
	if len(clusterScopedResources) == 0 {
		klog.Warning("Cluster scoped resources from shared cache empty.", user.csrErr)
		return user, user.csrErr
	}
	impersClientset, err := user.getImpersonationClientSet(clientToken, cache)
	if err != nil {
		user.csrErr = err
		klog.Warning("Error creating clientset with impersonation config.", err.Error())
		return user, user.csrErr
	}
	//If we have a new set of authorized list for the user reset the previous one
	user.csResources = nil
	for _, res := range clusterScopedResources {
		if user.userAuthorizedListCSResource(ctx, impersClientset, res.apigroup, res.kind) {
			user.csResources = append(user.csResources, resource{apigroup: res.apigroup, kind: res.kind})
		}
	}
	user.csrUpdatedAt = time.Now()
	return user, user.csrErr
}

func (user *userData) userAuthorizedListCSResource(ctx context.Context, authzClient v1.AuthorizationV1Interface,
	apigroup string, kind_plural string) bool {
	accessCheck := &authz.SelfSubjectAccessReview{
		Spec: authz.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authz.ResourceAttributes{
				Verb:     "list",
				Group:    apigroup,
				Resource: kind_plural,
			},
		},
	}
	result, err := authzClient.SelfSubjectAccessReviews().Create(ctx, accessCheck, metav1.CreateOptions{})

	if err != nil {
		klog.Error("Error creating SelfSubjectAccessReviews.", err)
	} else {
		klog.V(5).Infof("SelfSubjectAccessReviews API result for resource %s group %s : %v\n",
			kind_plural, apigroup, prettyPrint(result.Status.String()))
		if result.Status.Allowed {
			return true
		}
	}
	return false

}

// The following achieves same result as oc auth can-i --list -n <iterate-each-namespace>
func (user *userData) getNamespacedResources(cache *Cache, ctx context.Context, clientToken string) (*userData, error) {

	// getting the managed clusters
	managedClusterNamespaces, _ := user.getManagedClusters(ctx, cache, clientToken)

	// check if we already have user's namespaced resources in userData cache and check if time is expired
	user.nsrLock.Lock()
	defer user.nsrLock.Unlock()
	if len(user.nsResources) > 0 &&
		time.Now().Before(user.nsrUpdatedAt.Add(time.Duration(config.Cfg.UserCacheTTL)*time.Millisecond)) {
		klog.V(5).Info("Using user's namespaced resources from cache.")
		user.nsrErr = nil
		return user, user.nsrErr
	}

	// get all namespaces from shared cache:
	klog.V(5).Info("Getting namespaces from shared cache.")
	user.csrLock.Lock()
	defer user.csrLock.Unlock()
	allNamespaces := cache.shared.namespaces
	if len(allNamespaces) == 0 {
		klog.Warning("All namespaces array from shared cache is empty.", cache.shared.nsErr)
		return user, cache.shared.nsErr
	}

	user.csrErr = nil

	impersClientset, err := user.getImpersonationClientSet(clientToken, cache)
	if err != nil {
		klog.Warning("Error creating clientset with impersonation config.", err.Error())
		return user, err
	}

	user.nsResources = make(map[string][]resource)

	//get only the keys (names) from managedClusterNamespaces
	managedClusterNs := make([]string, 0, len(managedClusterNamespaces))
	for k := range managedClusterNamespaces {
		managedClusterNs = append(managedClusterNs, k)
	}

	allNamespaces, err = intersection(allNamespaces, managedClusterNs) //array of all managed clusters and namespaces per user
	if err != nil {
		klog.Warning("Error getting intersection of resources", err)
	}
	for _, ns := range allNamespaces {
		//
		rulesCheck := authz.SelfSubjectRulesReview{
			Spec: authz.SelfSubjectRulesReviewSpec{
				Namespace: ns,
			},
		}
		result, err := impersClientset.SelfSubjectRulesReviews().Create(ctx, &rulesCheck, metav1.CreateOptions{})
		if err != nil {
			klog.Error("Error creating SelfSubjectRulesReviews for namespace", err, ns)
		} else {
			klog.V(9).Infof("TokenReview Kube API result: %v\n", prettyPrint(result.Status))
		}
		for _, rules := range result.Status.ResourceRules { //iterate objects
			for _, verb := range rules.Verbs {
				if verb == "list" || verb == "*" { //drill down to list only
					for _, res := range rules.Resources {
						for _, api := range rules.APIGroups {
							user.nsResources[ns] = append(user.nsResources[ns], resource{apigroup: api, kind: res})
						}
					}
				}
			}

		}
	}

	fmt.Println("After SSRR:", user.nsResources)

	user.nsrUpdatedAt = time.Now()
	return user, user.nsrErr
}

func (user *userData) getImpersonationClientSet(clientToken string, cache *Cache) (v1.AuthorizationV1Interface,
	error) {
	if user.authzClient == nil {
		klog.V(5).Info("Creating New ImpersonationClientSet. ")
		restConfig := config.GetClientConfig()
		restConfig.Impersonate = rest.ImpersonationConfig{
			UserName: cache.tokenReviews[clientToken].tokenReview.Status.User.Username,
			UID:      cache.tokenReviews[clientToken].tokenReview.Status.User.UID,
		}
		clientset, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			klog.Error("Error with creating a new clientset with impersonation config.", err.Error())
			return nil, err
		}
		user.authzClient = clientset.AuthorizationV1()
	}
	return user.authzClient, nil
}

//For resources in the Managed Clusters search will show resources only if the user is authorized to see the managed cluster
func (user *userData) getManagedClusters(ctx context.Context, cache *Cache, clientToken string) (map[string]string, error) {

	// clusters lock
	user.clustersLock.Lock()
	defer user.clustersLock.Unlock()

	// check to see if we have any clusters in cache and if the update time has not expired
	if len(user.managedClusters) > 0 &&
		time.Now().Before(user.clustersUpdatedAt.Add(time.Duration(config.Cfg.UserCacheTTL)*time.Millisecond)) &&
		strings.Contains(user.getFromMap(user.managedClusters, "values")[0], "managedCluster") {
		klog.V(5).Info("Using user's managed clusters from cache.")
		user.clustersErr = nil
		return user.managedClusters, user.clustersErr
	}

	//get user's managed clusters and cache..
	klog.V(5).Info("Getting managed clusters from Kube Client..")

	// create a kubeclient (TODO: this we already do for the user so we should use the cached client in cache.client)
	cache.restConfig = config.GetClientConfig()
	clientset, err := kubernetes.NewForConfig(cache.restConfig)
	if err != nil {
		klog.Warning("Error with creating a new clientset.", err.Error())

	}

	user.managedClusters = make(map[string]string)
	// get namespacelist (TODO:this we already do above so we can combine (we don't need whole new function))
	namespaceList, _ := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})

	// var managedClusterNamespaces []string
	for _, namespace := range namespaceList.Items {
		for labels, _ := range namespace.Labels {
			if strings.Contains(labels, "managedCluster") {
				klog.V(9).Info("This label contains managedCluster:", namespace.Name)
				user.managedClusters[namespace.Name] = labels
				break
			}
		}
	}
	user.clustersUpdatedAt = time.Now()
	user.clustersErr = nil

	return user.managedClusters, user.clustersErr
}

//helper funtion to get intersection:
func intersection(a1, a2 []string) ([]string, error) {
	var intersection []string
	for _, x := range a1 {
		ok := false
		for _, y := range a2 {
			if x == y {
				ok = true
				break
			}
		}
		if ok {
			intersection = append(intersection, x)
		}
	}
	return intersection, nil
}

//helper function to get label values from managedCluster map:
func (user *userData) getFromMap(managedClusterNames map[string]string, mapPartStr string) []string {
	managedClusterNs := make([]string, 0, len(managedClusterNames))
	mapPart := mapPartStr
	switch mapPart {
	case "keys":
		//get only the keys (names) from managedClusterNamespaces
		for k, _ := range managedClusterNames {
			managedClusterNs = append(managedClusterNs, k)
		}
		return managedClusterNs
	case "values":
		//get only the values (labels) from managedClusterNamespaces
		for _, v := range managedClusterNames {
			managedClusterNs = append(managedClusterNs, v)
		}
		return managedClusterNs

	}
	return managedClusterNs
}