// Copyright 2022 Authors of spidernet-io
// SPDX-License-Identifier: Apache-2.0
package common

import (
	"context"
	"fmt"
	"math/rand"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	frame "github.com/spidernet-io/e2eframework/framework"
	corev1 "k8s.io/api/core/v1"
)

func GenerateString(lenNum int, isHex bool) string {
	var chars []string
	chars = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z", "A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z", "1", "2", "3", "4", "5", "6", "7", "8", "9", "0"}
	if isHex {
		chars = []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "a", "b", "c", "d", "e", "f"}
	}
	str := strings.Builder{}
	length := len(chars)
	rand.Seed(time.Now().UnixNano())
	for i := 0; i < lenNum; i++ {
		str.WriteString(chars[rand.Intn(length)])
	}
	return str.String()
}

func GenerateRandomNumber(max int) string {
	rand.Seed(time.Now().UnixNano())
	randomNumber := rand.Intn(max)
	return strconv.Itoa(randomNumber)
}

func CheckPodListInclude(list *corev1.PodList, pod *corev1.Pod) bool {
	tempMap := make(map[string]struct{})
	for _, p := range list.Items {
		tempMap[p.Name] = struct{}{}
	}
	_, ok := tempMap[pod.Name]
	return ok
}

func GetAdditionalPods(previous, latter *corev1.PodList) (pods []corev1.Pod) {
	for _, pod := range latter.Items {
		if !CheckPodListInclude(previous, &pod) {
			pods = append(pods, pod)
		}
	}
	return pods
}

func ExecCommandOnKindNode(ctx context.Context, nodeNameList []string, command string) error {
	for _, node := range nodeNameList {
		arg := fmt.Sprintf("docker exec -i %s %s", node, command)
		cmd := exec.Command("/bin/bash", "-c", arg)
		out, err := ExecCommand(ctx, cmd)
		GinkgoWriter.Printf("on node: %v, run cmd: %v, stdout: %v \n", node, cmd, out)
		if err != nil {
			return err
		}
	}
	return nil
}

func ExecCommand(ctx context.Context, cmd *exec.Cmd) (string, error) {
	var stdout string
	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	GinkgoWriter.Printf("run cmd: %v\n", cmd)
	Expect(err).NotTo(HaveOccurred())

	for {
		select {
		case <-ctx.Done():
			return stdout, frame.ErrTimeOut
		default:
			stdout = string(session.Out.Contents())
			exitCode := session.ExitCode()
			if exitCode == 0 {
				GinkgoWriter.Printf("exitCode: %v, stdout: %v\n", exitCode, stdout)
				return stdout, nil
			}
		}
		time.Sleep(time.Second)
	}
}
