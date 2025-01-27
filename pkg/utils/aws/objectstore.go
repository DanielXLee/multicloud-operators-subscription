// Copyright 2019 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"bytes"
	"context"
	"io/ioutil"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/endpoints"
	"github.com/aws/aws-sdk-go-v2/aws/external"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"k8s.io/klog"
)

// ObjectStore interface
type ObjectStore interface {
	InitObjectStoreConnection(endpoint, accessKeyID, secretAccessKey string) error
	Exists(bucket string) error
	Create(bucket string) error
	List(bucket string) ([]string, error)
	Put(bucket, name string, content []byte) error
	Delete(bucket, name string) error
	Get(bucket, name string) ([]byte, error)
}

var _ ObjectStore = &Handler{}

const (
	// SecretMapKeyAccessKeyID is key of accesskeyid in secret
	SecretMapKeyAccessKeyID = "AccessKeyID"
	// SecretMapKeySecretAccessKey is key of secretaccesskey in secret
	SecretMapKeySecretAccessKey = "SecretAccessKey"
)

// Handler handles connections to aws
type Handler struct {
	*s3.Client
}

// credentialProvider provides credetials for mcm hub deployable
type credentialProvider struct {
	AccessKeyID     string
	SecretAccessKey string
}

// Retrieve follow the Provider interface
func (p *credentialProvider) Retrieve() (aws.Credentials, error) {
	awscred := aws.Credentials{
		SecretAccessKey: p.SecretAccessKey,
		AccessKeyID:     p.AccessKeyID,
	}

	return awscred, nil
}

// InitObjectStoreConnection connect to object store
func (h *Handler) InitObjectStoreConnection(endpoint, accessKeyID, secretAccessKey string) error {
	klog.Info("Preparing S3 settings")

	cfg, err := external.LoadDefaultAWSConfig()

	if err != nil {
		klog.Error("Failed to load aws config. error: ", err)
		return err
	}
	// aws client report error without minio
	cfg.Region = "minio"

	defaultResolver := endpoints.NewDefaultResolver()
	s3CustResolverFn := func(service, region string) (aws.Endpoint, error) {
		if service == "s3" {
			return aws.Endpoint{
				URL: endpoint,
			}, nil
		}

		return defaultResolver.ResolveEndpoint(service, region)
	}

	cfg.EndpointResolver = aws.EndpointResolverFunc(s3CustResolverFn)
	cfg.Credentials = &credentialProvider{
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
	}

	h.Client = s3.New(cfg)
	if h.Client == nil {
		klog.Error("Failed to connect to s3 service")
		return err
	}

	h.Client.ForcePathStyle = true

	klog.V(2).Info("S3 configured ")

	return nil
}

// Create a bucket
func (h *Handler) Create(bucket string) error {
	req := h.Client.CreateBucketRequest(&s3.CreateBucketInput{
		Bucket: &bucket,
	})

	_, err := req.Send(context.TODO())
	if err != nil {
		klog.Error("Failed to create bucket ", bucket, ". error: ", err)
		return err
	}

	return nil
}

// Exists Checks whether a bucket exists and is accessible
func (h *Handler) Exists(bucket string) error {
	req := h.Client.HeadBucketRequest(&s3.HeadBucketInput{
		Bucket: &bucket,
	})

	_, err := req.Send(context.TODO())
	if err != nil {
		klog.Error("Failed to access bucket ", bucket, ". error: ", err)
		return err
	}

	return nil
}

// List all objects in bucket
func (h *Handler) List(bucket string) ([]string, error) {
	klog.V(10).Info("List S3 Objects ", bucket)

	req := h.Client.ListObjectsRequest(&s3.ListObjectsInput{Bucket: &bucket})
	p := s3.NewListObjectsPaginator(req)

	var keys []string

	for p.Next(context.TODO()) {
		page := p.CurrentPage()
		for _, obj := range page.Contents {
			keys = append(keys, *obj.Key)
		}
	}

	if err := p.Err(); err != nil {
		klog.Error("failed to list objects. error: ", err)
		return nil, err
	}

	klog.V(10).Info("List S3 Objects result ", keys)

	return keys, nil
}

// Get get existing object
func (h *Handler) Get(bucket, name string) ([]byte, error) {
	req := h.Client.GetObjectRequest(&s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &name,
	})

	resp, err := req.Send(context.Background())
	if err != nil {
		klog.Error("Failed to send Get request. error: ", err)
		return nil, err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		klog.Error()
	}

	klog.V(5).Info("Object Store Get Success: \n", string(body))

	return body, nil
}

// Put create new object
func (h *Handler) Put(bucket, name string, content []byte) error {
	req := h.Client.PutObjectRequest(&s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &name,
		Body:   bytes.NewReader(content),
	})

	resp, err := req.Send(context.Background())
	if err != nil {
		klog.Error("Failed to send Put request. error: ", err)
		return err
	}

	klog.V(10).Info("Put Success", resp)

	return nil
}

// Delete delete existing object
func (h *Handler) Delete(bucket, name string) error {
	req := h.Client.DeleteObjectRequest(&s3.DeleteObjectInput{
		Bucket: &bucket,
		Key:    &name,
	})

	resp, err := req.Send(context.Background())
	if err != nil {
		klog.Error("Failed to send Delete request. error: ", err)
		return err
	}

	klog.V(10).Info("Delete Success", resp)

	return nil
}
