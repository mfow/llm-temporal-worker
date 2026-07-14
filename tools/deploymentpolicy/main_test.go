package main

import (
	"strings"
	"testing"
)

func TestVerifyRenderedAcceptsBaseWorkloadPolicy(t *testing.T) {
	if err := verifyRendered("base", []byte(validRenderedWorkload)); err != nil {
		t.Fatalf("verify rendered base workload: %v", err)
	}
}

func TestVerifyRenderedRejectsWorkloadPolicyViolations(t *testing.T) {
	tests := []struct {
		name    string
		change  func(string) string
		message string
	}{
		{
			name: "root UID",
			change: func(value string) string {
				return strings.Replace(value, "runAsUser: 65532", "runAsUser: 0", 1)
			},
			message: "positive numeric UID",
		},
		{
			name: "string UID",
			change: func(value string) string {
				return strings.Replace(value, "runAsUser: 65532", "runAsUser: \"65532\"", 1)
			},
			message: "positive numeric UID",
		},
		{
			name: "unexpected group",
			change: func(value string) string {
				return strings.Replace(value, "runAsGroup: 65532", "runAsGroup: 1", 1)
			},
			message: "numeric GID 65532",
		},
		{
			name: "missing file-system group",
			change: func(value string) string {
				return strings.Replace(value, "        fsGroup: 65532\n", "", 1)
			},
			message: "numeric fsGroup 65532",
		},
		{
			name: "writable root",
			change: func(value string) string {
				return strings.Replace(value, "readOnlyRootFilesystem: true", "readOnlyRootFilesystem: false", 1)
			},
			message: "root filesystem",
		},
		{
			name: "unbounded temporary storage",
			change: func(value string) string {
				return strings.Replace(value, "\n            sizeLimit: 128Mi", "", 1)
			},
			message: "bounded memory emptyDir",
		},
		{
			name: "additional writable mount",
			change: func(value string) string {
				return strings.Replace(value, "mountPath: /etc/llmtw\n              readOnly: true", "mountPath: /etc/llmtw\n              readOnly: false", 1)
			},
			message: "must be read-only",
		},
		{
			name: "unbounded resources",
			change: func(value string) string {
				return strings.Replace(value, "\n            limits:\n              cpu: \"2\"\n              memory: 2Gi", "", 1)
			},
			message: "limits must bound CPU and memory",
		},
		{
			name: "wrong readiness endpoint",
			change: func(value string) string {
				return strings.Replace(value, "path: /health/ready", "path: /health/live", 1)
			},
			message: "/health/ready",
		},
		{
			name: "wrong health container port",
			change: func(value string) string {
				return strings.Replace(value, "containerPort: 8080", "containerPort: 80", 1)
			},
			message: "named health port",
		},
		{
			name: "wrong liveness port reference",
			change: func(value string) string {
				return strings.Replace(value, "path: /health/live\n              port: health", "path: /health/live\n              port: 9999", 1)
			},
			message: "health port",
		},
		{
			name: "unapproved service account token",
			change: func(value string) string {
				return strings.Replace(value, "automountServiceAccountToken: false", "automountServiceAccountToken: true", -1)
			},
			message: "only in a workload identity overlay",
		},
		{
			name: "init container",
			change: func(value string) string {
				return strings.Replace(value, "      containers:\n", "      initContainers:\n        - name: unsafe\n          image: busybox@sha256:REPLACE_WITH_RELEASE_DIGEST\n      containers:\n", 1)
			},
			message: "init containers are not allowed",
		},
		{
			name: "additional container",
			change: func(value string) string {
				return strings.Replace(value, "      volumes:\n", "        - name: sidecar\n          image: busybox@sha256:REPLACE_WITH_RELEASE_DIGEST\n      volumes:\n", 1)
			},
			message: "exactly one container",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := verifyRendered("base", []byte(test.change(validRenderedWorkload)))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("verify rendered base workload error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestVerifyRenderedAllowsOnlyAnnotatedIdentityOverlays(t *testing.T) {
	awsWorkload := strings.ReplaceAll(validRenderedWorkload, "automountServiceAccountToken: false", "automountServiceAccountToken: true")
	awsWorkload = strings.Replace(awsWorkload, "metadata:\n  name: llmtw-worker\nautomount", "metadata:\n  name: llmtw-worker\n  annotations:\n    eks.amazonaws.com/role-arn: arn:aws:iam::REPLACE_ACCOUNT:role/REPLACE_ROLE\nautomount", 1)
	if err := verifyRendered("aws-workload-identity", []byte(awsWorkload)); err != nil {
		t.Fatalf("verify rendered AWS workload identity: %v", err)
	}

	if err := verifyRendered("azure-workload-identity", []byte(awsWorkload)); err == nil || !strings.Contains(err.Error(), "Azure workload identity") {
		t.Fatalf("verify rendered Azure workload identity error = %v", err)
	}
}

const validRenderedWorkload = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: llmtw-worker
automountServiceAccountToken: false
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: llmtw-worker
spec:
  template:
    spec:
      serviceAccountName: llmtw-worker
      automountServiceAccountToken: false
      terminationGracePeriodSeconds: 90
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: worker
          image: ghcr.io/mfow/llm-temporal-worker@sha256:REPLACE_WITH_RELEASE_DIGEST
          ports:
            - name: health
              containerPort: 8080
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
          resources:
            requests:
              cpu: 250m
              memory: 512Mi
            limits:
              cpu: "2"
              memory: 2Gi
          livenessProbe:
            httpGet:
              path: /health/live
              port: health
          readinessProbe:
            httpGet:
              path: /health/ready
              port: health
          volumeMounts:
            - name: config
              mountPath: /etc/llmtw
              readOnly: true
            - name: runtime-secrets
              mountPath: /var/run/secrets/llmtw
              readOnly: true
            - name: tmp
              mountPath: /tmp
      volumes:
        - name: config
          configMap:
            name: llmtw-config
        - name: runtime-secrets
          secret:
            secretName: llmtw-worker-secrets
        - name: tmp
          emptyDir:
            medium: Memory
            sizeLimit: 128Mi
`
