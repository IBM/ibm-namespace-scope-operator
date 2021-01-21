module github.com/IBM/ibm-namespace-scope-operator

go 1.13

require (
	github.com/IBM/controller-filtered-cache v0.2.1
	github.com/deckarep/golang-set v1.7.1
	github.com/onsi/ginkgo v1.12.1
	github.com/onsi/gomega v1.10.1
	k8s.io/api v0.18.6
	k8s.io/apimachinery v0.18.6
	k8s.io/client-go v0.18.6
	k8s.io/klog v1.0.0
	sigs.k8s.io/controller-runtime v0.6.2
)

// fix vulnerability: CVE-2021-3121 in github.com/gogo/protobuf < v1.3.2
replace github.com/gogo/protobuf => github.com/gogo/protobuf v1.3.2
