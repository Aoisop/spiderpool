// Copyright 2022 Authors of spidernet-io
// SPDX-License-Identifier: Apache-2.0
package reclaim_test

import (
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	spiderpool "github.com/spidernet-io/spiderpool/pkg/k8s/apis/spiderpool.spidernet.io/v1"
	"golang.org/x/net/context"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/spidernet-io/e2eframework/tools"
	"github.com/spidernet-io/spiderpool/pkg/constant"
	"github.com/spidernet-io/spiderpool/test/e2e/common"
)

var _ = Describe("test ip with reclaim ip case", Label("reclaim"), func() {
	var err error
	var podName, namespace string

	BeforeEach(func() {
		// Init test info and create namespace
		podName = "pod" + tools.RandomName()
		namespace = "ns" + tools.RandomName()
		GinkgoWriter.Printf("create namespace %v \n", namespace)
		err = frame.CreateNamespaceUntilDefaultServiceAccountReady(namespace, common.ServiceAccountReadyTimeout)
		Expect(err).NotTo(HaveOccurred(), "failed to create namespace %v", namespace)

		DeferCleanup(func() {
			GinkgoWriter.Printf("delete namespace %v \n", namespace)
			err := frame.DeleteNamespace(namespace)
			Expect(err).NotTo(HaveOccurred(), "failed to delete namespace %v", namespace)
		})
	})

	DescribeTable("check ip release after job finished", func(behavior common.JobBehave) {
		var wg sync.WaitGroup
		var jdName string = "jd" + tools.RandomName()
		var jobHoldDuration = "sleep 5;"

		// Generate job yaml with different behaviour and create it
		GinkgoWriter.Printf("try to create Job %v/%v with job behavior is %v \n", namespace, jdName, behavior)
		jd := common.GenerateExampleJobYaml(behavior, jdName, namespace, pointer.Int32Ptr(2))
		switch behavior {
		case common.JobTypeFail:
			jd.Spec.Template.Spec.Containers[0].Command = []string{"/bin/sh", "-c", jobHoldDuration + "exit 1"}
		case common.JobTypeFinish:
			jd.Spec.Template.Spec.Containers[0].Command = []string{"/bin/sh", "-c", jobHoldDuration + " exit 0"}
		}
		Expect(frame.CreateJob(jd)).NotTo(HaveOccurred(), "Failed to create job %v/%v \n", namespace, jdName)

		// Confirm that the `failed` and `succeeded` job have been assigned an IP address before the job finish
		ctx, cancel := context.WithTimeout(context.Background(), common.PodStartTimeout)
		defer cancel()
		wg.Add(1)
		go func() {
			defer GinkgoRecover()
		OUTER:
			for {
				select {
				case <-ctx.Done():
					Fail("IPs allocated during their validity period are not recorded in the ippool.")
				default:
					podList, err := frame.GetJobPodList(jd)
					Expect(err).NotTo(HaveOccurred())
					if len(podList.Items) == 0 {
						continue OUTER
					}
					GinkgoWriter.Printf("Confirm that the job has been assigned to the IP address \n")
					ok, _, _, err := common.CheckPodIpRecordInIppool(frame, ClusterDefaultV4IpoolList, ClusterDefaultV6IpoolList, podList)
					if !ok || err != nil {
						continue OUTER
					}
				}
				break OUTER
			}
			wg.Done()
		}()

		// Waiting for different behaviour job to be finished
		GinkgoWriter.Printf("Waiting for job to be finished and behaviour is %v \n", behavior)
		ctx1, cancel1 := context.WithTimeout(context.Background(), common.PodStartTimeout)
		defer cancel1()
		jb, ok1, e := frame.WaitJobFinished(jdName, namespace, ctx1)
		Expect(e).NotTo(HaveOccurred())
		Expect(jb).NotTo(BeNil())
		wg.Wait()

		switch behavior {
		case common.JobTypeFail:
			Expect(ok1).To(BeFalse())
		case common.JobTypeFinish:
			Expect(ok1).To(BeTrue())
		default:
			Fail("input error")
		}
		GinkgoWriter.Printf("job %v/%v is finished, job conditions is : %v \n", namespace, jb, jb.Status.Conditions)

		// The IP should be reclaimed for the job pod finished with success or failure Status
		GinkgoWriter.Printf("Get a list of pods when the job is done \n")
		podlist, err := frame.GetJobPodList(jd)
		Expect(err).NotTo(HaveOccurred())
		Expect(podlist.Items).NotTo(HaveLen(0))

		GinkgoWriter.Println("The IP should be reclaimed for the job pod finished with success or failure Status")
		Expect(common.WaitIPReclaimedFinish(frame, ClusterDefaultV4IpoolList, ClusterDefaultV6IpoolList, podlist, time.Minute*2)).To(Succeed())

		GinkgoWriter.Printf("delete job: %v \n", jdName)
		Expect(frame.DeleteJob(jdName, namespace)).NotTo(HaveOccurred(), "failed to delete job: %v \n", jdName)
	},
		Entry("check ip release when job is failed", Label("G00006"), common.JobTypeFail),
		Entry("check ip release when job is succeeded", Label("G00006"), common.JobTypeFinish),
	)

	It(`G00002:the IP of a running pod should not be reclaimed after a same-name pod within a different namespace is deleted;
		G00001:related IP resource recorded in ippool will be reclaimed after the namespace is deleted`, Label("G00002", "G00001", "smoke"), func() {
		var podList *corev1.PodList
		var namespace1 string = "ns1-" + tools.RandomName()

		// Create a namespace again and name it namespace1
		GinkgoWriter.Printf("create namespace1 %v \n", namespace1)
		err = frame.CreateNamespaceUntilDefaultServiceAccountReady(namespace1, common.ServiceAccountReadyTimeout)
		Expect(err).NotTo(HaveOccurred(), "failed to create namespace1 %v", namespace1)

		namespaces := []string{namespace, namespace1}
		for _, ns := range namespaces {
			// Create pods with the same name in different namespaces
			podYaml := common.GenerateExamplePodYaml(podName, ns)
			Expect(podYaml).NotTo(BeNil())
			pod, _, _ := common.CreatePodUntilReady(frame, podYaml, podName, ns, common.PodStartTimeout)
			Expect(pod).NotTo(BeNil(), "Failed to create Pod")
			// Construct a podlist for another namespace（namepace1）
			if ns == namespace1 {
				podList = &corev1.PodList{
					Items: []corev1.Pod{*pod},
				}
			}
		}

		// Delete pods from namespace until complete
		GinkgoWriter.Printf("Delete Pods %v/%v from namespace %v until complete \n", podName, namespace)
		ctx, cancel := context.WithTimeout(context.Background(), common.PodReStartTimeout)
		defer cancel()
		e2 := frame.DeletePodUntilFinish(podName, namespace, ctx)
		Expect(e2).NotTo(HaveOccurred())
		GinkgoWriter.Printf("Successful deletion of pods %v/%v \n", namespace, podName)

		// Another Pod in namespace1 with the same name has a normal status and IP
		Expect(frame.CheckPodListIpReady(podList)).NotTo(HaveOccurred())
		ok, _, _, err := common.CheckPodIpRecordInIppool(frame, ClusterDefaultV4IpoolList, ClusterDefaultV6IpoolList, podList)
		Expect(err).NotTo(HaveOccurred(), "error: %v\n", err)
		Expect(ok).To(BeTrue())

		By("G00001: related IP resource recorded in ippool will be reclaimed after the namespace is deleted")
		// Try to delete namespace1
		err = frame.DeleteNamespace(namespace1)
		Expect(err).NotTo(HaveOccurred(), "failed to delete namespace %v", namespace1)
		GinkgoWriter.Printf("Successfully deleted namespace %v \n", namespace1)

		// Check that the Pod IPs in the IPPool are reclaimed properly after deleting the namespace（namespace1）
		Expect(common.WaitIPReclaimedFinish(frame, ClusterDefaultV4IpoolList, ClusterDefaultV6IpoolList, podList, common.IPReclaimTimeout)).To(Succeed())
	})

	It("the IP can be reclaimed after its deployment, statefulSet, daemonSet, replicaSet, or job is deleted, even when CNI binary is gone on the host",
		// E00001: Assign IP to a pod for ipv4, ipv6 and dual-stack case
		// E00002: Assign IP to deployment/pod for ipv4, ipv6 and dual-stack case
		// E00003: Assign IP to statefulSet/pod for ipv4, ipv6 and dual-stack case
		// E00004: Assign IP to daemonSet/pod for ipv4, ipv6 and dual-stack case
		// E00005: Assign IP to job/pod for ipv4, ipv6 and dual-stack case
		// E00006: Assign IP to replicaset/pod for ipv4, ipv6 and dual-stack case
		Label("G00003", "G00004", "E00001", "E00002", "E00003", "E00004", "E00005", "E00006", "smoke"), Serial, func() {
			var podList *corev1.PodList
			var (
				deployName     string = "deploy-" + tools.RandomName()
				depReplicasNum int32  = 1
				stsName        string = "sts-" + tools.RandomName()
				stsReplicasNum int32  = 1
				dsName         string = "ds-" + tools.RandomName()
				rsName         string = "rs-" + tools.RandomName()
				rsReplicasNum  int32  = 1
				jobName        string = "job-" + tools.RandomName()
				jobNum         int32  = *pointer.Int32Ptr(1)
			)

			// Create different controller resources
			// Generate example podYaml and create Pod
			podYaml := common.GenerateExamplePodYaml(podName, namespace)
			Expect(podYaml).NotTo(BeNil())
			GinkgoWriter.Printf("try to create Pod %v/%v \n", namespace, podName)
			Expect(frame.CreatePod(podYaml)).NotTo(HaveOccurred(), "failed to create Pod %v/%v \n", namespace, podName)

			// Generate example StatefulSet yaml and create StatefulSet
			GinkgoWriter.Printf("Generate example StatefulSet %v/%v yaml \n", namespace, stsName)
			stsYaml := common.GenerateExampleStatefulSetYaml(stsName, namespace, stsReplicasNum)
			Expect(stsYaml).NotTo(BeNil(), "failed to generate example %v/%v yaml \n", namespace, stsName)
			GinkgoWriter.Printf("Tty to create StatefulSet %v/%v \n", namespace, stsName)
			Expect(frame.CreateStatefulSet(stsYaml)).To(Succeed(), "failed to create StatefulSet %v/%v \n", namespace, stsName)

			// Generate example daemonSet yaml and create daemonSet
			GinkgoWriter.Printf("Generate example daemonSet %v/%v yaml\n", namespace, dsName)
			dsYaml := common.GenerateExampleDaemonSetYaml(dsName, namespace)
			Expect(dsYaml).NotTo(BeNil(), "failed to generate example daemonSet %v/%v yaml\n", namespace, dsName)
			GinkgoWriter.Printf("Try to create daemonSet %v/%v \n", namespace, dsName)
			Expect(frame.CreateDaemonSet(dsYaml)).To(Succeed(), "failed to create daemonSet %v/%v \n", namespace, dsName)

			// Generate example replicaSet yaml and create replicaSet
			GinkgoWriter.Printf("Generate example replicaSet %v/%v yaml \n", namespace, rsName)
			rsYaml := common.GenerateExampleReplicaSetYaml(rsName, namespace, rsReplicasNum)
			Expect(rsYaml).NotTo(BeNil(), "failed to generate replicaSet example %v/%v yaml \n", namespace, rsName)
			GinkgoWriter.Printf("Try to create replicaSet %v/%v \n", namespace, rsName)
			Expect(frame.CreateReplicaSet(rsYaml)).To(Succeed(), "failed to create replicaSet %v/%v \n", namespace, rsName)

			// Generate example Deployment yaml and create Deployment
			GinkgoWriter.Printf("Generate example deployment %v/%v yaml \n", namespace, deployName)
			deployYaml := common.GenerateExampleDeploymentYaml(deployName, namespace, depReplicasNum)
			Expect(deployYaml).NotTo(BeNil(), "failed to generate deployment example %v/%v yaml \n", namespace, deployName)
			GinkgoWriter.Printf("Try to create deployment %v/%v \n", namespace, deployName)
			Expect(frame.CreateDeployment(deployYaml)).NotTo(HaveOccurred())

			// Generate example job yaml and create Job resource
			GinkgoWriter.Printf("Generate example job %v/%v yaml\n", namespace, jobName)
			jobYaml := common.GenerateExampleJobYaml(common.JobTypeRunningForever, jobName, namespace, &jobNum)
			Expect(jobYaml).NotTo(BeNil(), "failed to generate job example %v/%v yaml \n", namespace, jobName)
			GinkgoWriter.Printf("Try to create job %v/%v \n", namespace, jobName)
			Expect(frame.CreateJob(jobYaml)).To(Succeed(), "failed to create job %v/%v \n", namespace, jobName)

			ctx, cancel := context.WithTimeout(context.Background(), common.PodStartTimeout)
			defer cancel()
		LOOP:
			for {
				select {
				case <-ctx.Done():
					Fail("time out to wait all controller pod running \n")
				default:
					// Get all the pods created under namespace and check if the status is running
					podList, err = frame.GetPodList(client.InNamespace(namespace))
					Expect(err).NotTo(HaveOccurred(), "failed to get podList, error: %v \n", err)
					Expect(podList.Items).NotTo(HaveLen(0))
					// TODO(tao.yang), Inability to accurately sense the cause of failure
					isOk := frame.CheckPodListRunning(podList)
					if !isOk {
						time.Sleep(common.ForcedWaitingTime)
						continue LOOP
					}
					break LOOP
				}
			}

			// Please note that the following checkpoints for cases are included here
			// E00001、E00002、E00003、E00004、E00005、E00006
			// Check the resource ip information in ippool is correct
			GinkgoWriter.Printf("check the ip information of resources in the nippool is correct \n", namespace)
			ok, _, _, err := common.CheckPodIpRecordInIppool(frame, ClusterDefaultV4IpoolList, ClusterDefaultV6IpoolList, podList)
			Expect(err).NotTo(HaveOccurred(), "failed to check ip recorded in ippool, err: %v\n", err)
			Expect(ok).To(BeTrue())

			// remove cni bin
			GinkgoWriter.Println("remove cni bin")
			command := "mv /opt/cni/bin/multus /opt/cni/bin/multus.backup"
			ctx, cancel = context.WithTimeout(context.Background(), common.ExecCommandTimeout)
			defer cancel()
			err = common.ExecCommandOnKindNode(ctx, frame.Info.KindNodeList, command)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Println("remove cni bin successfully")

			By("G00004: The IP should be reclaimed when deleting the pod with 0 second of grace period")
			opt := &client.DeleteOptions{
				GracePeriodSeconds: pointer.Int64Ptr(0),
			}
			// Delete resources with 0 second of grace period
			GinkgoWriter.Printf("delete pod %v/%v\n", namespace, podName)
			Expect(frame.DeletePod(podName, namespace, opt)).NotTo(HaveOccurred())

			GinkgoWriter.Printf("delete deployment %v/%v\n", namespace, deployName)
			Expect(frame.DeleteDeployment(deployName, namespace, opt)).To(Succeed())

			GinkgoWriter.Printf("delete statefulSet %v/%v\n", namespace, stsName)
			Expect(frame.DeleteStatefulSet(stsName, namespace, opt)).To(Succeed())

			GinkgoWriter.Printf("delete daemonSet %v/%v\n", namespace, dsName)
			Expect(frame.DeleteDaemonSet(dsName, namespace, opt)).To(Succeed())

			GinkgoWriter.Printf("delete replicaset %v/%v\n", namespace, rsName)
			Expect(frame.DeleteReplicaSet(rsName, namespace, opt)).To(Succeed())

			GinkgoWriter.Printf("delete job %v/%v\n", namespace, jobName)
			Expect(frame.DeleteJob(jobName, namespace, opt)).To(Succeed())

			// avoid that "GracePeriodSeconds" of workload does not take effect
			podList, err = frame.GetPodList(client.InNamespace(namespace))
			Expect(err).NotTo(HaveOccurred(), "failed to get podList, error: %v\n", err)
			Expect(frame.DeletePodList(podList, opt)).To(Succeed(), "failed to delete podList\n")

			// Check that the IP in the IPPool has been reclaimed correctly.
			Expect(common.WaitIPReclaimedFinish(frame, ClusterDefaultV4IpoolList, ClusterDefaultV6IpoolList, podList, common.IPReclaimTimeout)).To(Succeed())
			GinkgoWriter.Println("Delete resource with 0 second grace period where the IP of the resource is correctly reclaimed")

			// restore cni bin
			GinkgoWriter.Println("restore cni bin")
			command = "mv /opt/cni/bin/multus.backup /opt/cni/bin/multus"
			ctx2, cancel2 := context.WithTimeout(context.Background(), common.ExecCommandTimeout)
			defer cancel2()
			err = common.ExecCommandOnKindNode(ctx2, frame.Info.KindNodeList, command)
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Println("restore cni bin successfully")

			// cni wait for node ready after recovery
			GinkgoWriter.Println("wait cluster node ready")
			ctx3, cancel3 := context.WithTimeout(context.Background(), common.NodeReadyTimeout)
			defer cancel3()
			ok, err = frame.WaitClusterNodeReady(ctx3)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
		})

	Context("test the reclaim of dirty IP record in the IPPool", func() {
		var v4poolName, v6poolName string
		var v4poolNameList, v6poolNameList []string
		var v4poolObj, v6poolObj *spiderpool.SpiderIPPool
		var (
			podIPv4Record, podIPv6Record     = new(spiderpool.PoolIPAllocation), new(spiderpool.PoolIPAllocation)
			dirtyIPv4Record, dirtyIPv6Record = new(spiderpool.PoolIPAllocation), new(spiderpool.PoolIPAllocation)
		)
		var dirtyIPv4, dirtyIPv6 string
		var dirtyPodName, dirtyContainerID string

		BeforeEach(func() {
			// generate dirty ip, pod name and dirty containerID
			dirtyIPv4 = common.GenerateExampleIpv4Address()
			GinkgoWriter.Printf("generate dirty IPv4 :%v \n", dirtyIPv4)

			dirtyIPv6 = common.GenerateExampleIpv4Address()
			GinkgoWriter.Printf("generate dirty IPv6:%v\n", dirtyIPv6)

			dirtyPodName = "dirtyPod-" + tools.RandomName()
			GinkgoWriter.Printf("generate dirty Pod name: %v \n", dirtyPodName)

			dirtyContainerID = common.GenerateString(64, true)
			GinkgoWriter.Printf("generate dirty containerID: %v\n", dirtyContainerID)

			// Create IPv4Pool and IPV6Pool
			if frame.Info.IpV4Enabled {
				GinkgoWriter.Println("create ipv4 pool")
				v4poolName, v4poolObj = common.GenerateExampleIpv4poolObject(1)
				v4poolNameList = []string{v4poolName}
				Expect(common.CreateIppool(frame, v4poolObj)).To(Succeed())
			}
			if frame.Info.IpV6Enabled {
				GinkgoWriter.Println("create ipv6 pool")
				v6poolName, v6poolObj = common.GenerateExampleIpv6poolObject(1)
				v6poolNameList = []string{v6poolName}
				Expect(common.CreateIppool(frame, v6poolObj)).To(Succeed())
			}
			DeferCleanup(func() {
				GinkgoWriter.Printf("Try to delete IPv4 %v, IPv6 %v IPPool \n", v4poolName, v6poolName)
				if frame.Info.IpV4Enabled {
					err := common.DeleteIPPoolByName(frame, v4poolName)
					Expect(err).NotTo(HaveOccurred())
				}
				if frame.Info.IpV6Enabled {
					err := common.DeleteIPPoolByName(frame, v6poolName)
					Expect(err).NotTo(HaveOccurred())
				}
			})
		})
		DescribeTable("dirty IP record in the IPPool should be auto clean by Spiderpool", func() {
			// Generate IPPool annotation string
			podIppoolAnnoStr := common.GeneratePodIPPoolAnnotations(frame, common.NIC1, v4poolNameList, v6poolNameList)

			// generate podYaml and create pod
			podYaml := common.GenerateExamplePodYaml(podName, namespace)
			Expect(podYaml).NotTo(BeNil())
			podYaml.Annotations = map[string]string{constant.AnnoPodIPPool: podIppoolAnnoStr}
			GinkgoWriter.Printf("create pod %v/%v \n", namespace, podName)
			pod, podIPv4, podIPv6 := common.CreatePodUntilReady(frame, podYaml, podName, namespace, common.PodStartTimeout)
			Expect(pod).NotTo(BeNil())
			GinkgoWriter.Printf("podIPv4: %v; podIPv6 %v \n", podIPv4, podIPv6)

			// check pod ip in ippool
			GinkgoWriter.Printf("check pod %v/%v ip in ippool\n", namespace, podName)
			podList := &corev1.PodList{Items: []corev1.Pod{*pod}}
			allRecorded, _, _, err := common.CheckPodIpRecordInIppool(frame, v4poolNameList, v6poolNameList, podList)
			Expect(allRecorded).To(BeTrue())
			Expect(err).NotTo(HaveOccurred())

			dirtyIPRecordList := []string{"Pod", "ContainerID"}
			for _, v := range dirtyIPRecordList {
				// get pod ip record in ippool
				if frame.Info.IpV4Enabled {
					GinkgoWriter.Printf("get pod=%v/%v ip=%v record in ipv4 pool=%v\n", namespace, podName, podIPv4, v4poolName)
					v4poolObj = common.GetIppoolByName(frame, v4poolName)
					*podIPv4Record = v4poolObj.Status.AllocatedIPs[podIPv4]
					GinkgoWriter.Printf("the pod ip record in ipv4 pool is %v\n", *podIPv4Record)
				}
				if frame.Info.IpV6Enabled {
					GinkgoWriter.Printf("get pod=%v/%v ip=%v record in ipv6 pool=%v\n", namespace, podName, podIPv6, v6poolName)
					v6poolObj = common.GetIppoolByName(frame, v6poolName)
					*podIPv6Record = v6poolObj.Status.AllocatedIPs[podIPv6]
					GinkgoWriter.Printf("the pod ip record in ipv6 pool is %v\n", *podIPv6Record)
				}

				GinkgoWriter.Println("add dirty data to ippool")
				if frame.Info.IpV4Enabled {
					dirtyIPv4Record = podIPv4Record
					if v == "Pod" {
						dirtyIPv4Record.Pod = dirtyPodName
					} else {
						dirtyIPv4Record.ContainerID = dirtyContainerID
					}
					allocatedIPCount := *v4poolObj.Status.AllocatedIPCount
					allocatedIPCount++
					GinkgoWriter.Printf("allocatedIPCount: %v\n", allocatedIPCount)
					v4poolObj.Status.AllocatedIPCount = pointer.Int64Ptr(allocatedIPCount)

					v4poolObj.Status.AllocatedIPs[dirtyIPv4] = *dirtyIPv4Record

					// Update dirty data to IPv4 IPPool.Status.AllocatedIPs
					GinkgoWriter.Printf("update ippool %v for adding dirty record: %+v \n", v4poolName, *dirtyIPv4Record)
					Expect(frame.UpdateResourceStatus(v4poolObj)).To(Succeed())
					GinkgoWriter.Printf("ipv4 pool %+v\n", v4poolObj)

					// check if dirty data added successfully
					v4poolObj = common.GetIppoolByName(frame, v4poolName)
					record, ok := v4poolObj.Status.AllocatedIPs[dirtyIPv4]
					Expect(ok).To(BeTrue())
					Expect(record).To(Equal(*dirtyIPv4Record))
				}

				if frame.Info.IpV6Enabled {
					dirtyIPv6Record = podIPv6Record
					if v == "Pod" {
						dirtyIPv6Record.Pod = dirtyPodName
					} else {
						dirtyIPv6Record.ContainerID = dirtyContainerID
					}
					allocatedIPCount := *v6poolObj.Status.AllocatedIPCount
					allocatedIPCount++
					GinkgoWriter.Printf("allocatedIPCount: %v\n", allocatedIPCount)
					v6poolObj.Status.AllocatedIPCount = pointer.Int64Ptr(allocatedIPCount)

					v6poolObj.Status.AllocatedIPs[dirtyIPv6] = *dirtyIPv6Record

					// Update dirty data to IPv6 IPPool.Status.AllocatedIPs
					GinkgoWriter.Printf("update ippool %v for adding dirty record: %+v \n", v6poolName, *dirtyIPv6Record)
					Expect(frame.UpdateResourceStatus(v6poolObj)).To(Succeed())
					GinkgoWriter.Printf("ipv6 pool %+v\n", v6poolObj)

					// Check if dirty data added successfully
					v6poolObj = common.GetIppoolByName(frame, v6poolName)
					record, ok := v6poolObj.Status.AllocatedIPs[dirtyIPv6]
					Expect(ok).To(BeTrue())
					Expect(record).To(Equal(*dirtyIPv6Record))
				}
				// restart spiderpool controller to trigger gc
				GinkgoWriter.Println("restart spiderpool controller")
				spiderpoolControllerPodList, err := frame.GetPodListByLabel(map[string]string{"app.kubernetes.io/component": "spiderpool-controller"})
				Expect(err).NotTo(HaveOccurred())
				Expect(spiderpoolControllerPodList).NotTo(BeNil(), "failed to get spiderpool controller podList \n")
				Expect(spiderpoolControllerPodList.Items).NotTo(BeEmpty(), "failed to get spiderpool controller podList \n")
				spiderpoolControllerPodList, err = frame.DeletePodListUntilReady(spiderpoolControllerPodList, common.PodReStartTimeout)
				Expect(err).NotTo(HaveOccurred())
				Expect(spiderpoolControllerPodList).NotTo(BeNil(), "failed to get spiderpool controller podList after restart \n")
				Expect(spiderpoolControllerPodList.Items).NotTo(HaveLen(0), "failed to get spiderpool controller podList \n")
				GinkgoWriter.Println("succeed to restart spiderpool controller pods")

				// check the real pod ip should be recorded in spiderpool, the dirty ip record should be reclaimed from spiderpool
				GinkgoWriter.Printf("check if the pod %v/%v ip recorded in ippool, check if the dirty ip record reclaimed from ippool\n", namespace, podName)
				if frame.Info.IpV4Enabled {
					// check if dirty IPv4 data reclaimed successfully
					ctx, cancel := context.WithTimeout(context.Background(), common.IPReclaimTimeout)
					defer cancel()
					Expect(common.WaitIppoolStatusConditionByAllocatedIPs(ctx, frame, v4poolName, dirtyIPv6, false)).NotTo(HaveOccurred())
				}
				if frame.Info.IpV6Enabled {
					// check if dirty IPv6 data reclaimed successfully
					ctx, cancel := context.WithTimeout(context.Background(), common.IPReclaimTimeout)
					defer cancel()
					Expect(common.WaitIppoolStatusConditionByAllocatedIPs(ctx, frame, v6poolName, dirtyIPv6, false)).NotTo(HaveOccurred())
				}
			}
			// Check Pod IP in IPPool
			allRecorded, _, _, err = common.CheckPodIpRecordInIppool(frame, v4poolNameList, v6poolNameList, podList)
			Expect(allRecorded).To(BeTrue())
			Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("succeed to check pod %v/%v ip in ippool\n", namespace, podName)

			// Delete Pod
			Expect(frame.DeletePod(podName, namespace)).To(Succeed(), "Failed to delete pod %v/%v\n", namespace, podName)
			GinkgoWriter.Printf("succeed to delete pod %v/%v\n", namespace, podName)
		},
			Entry("a dirty IP record (pod name is wrong or containerID is wrong) in the IPPool should be auto clean by Spiderpool", Serial, Label("G00005", "G00007")),
		)
	})
})
