// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Posta.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package blob

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type s3Store struct {
	client *s3.Client
	bucket string
}

func newS3Store(cfg Config) (*s3Store, error) {
	if cfg.S3Bucket == "" {
		return nil, fmt.Errorf("S3 bucket name is required")
	}

	opts := func(o *s3.Options) {
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		}
		if cfg.S3Region != "" {
			o.Region = cfg.S3Region
		} else {
			o.Region = "us-east-1"
		}
		if cfg.S3AccessKeyID != "" {
			o.Credentials = credentials.NewStaticCredentialsProvider(
				cfg.S3AccessKeyID,
				cfg.S3SecretAccessKey,
				"",
			)
		}
		o.UsePathStyle = cfg.S3ForcePathStyle
	}

	client := s3.New(s3.Options{}, opts)

	return &s3Store{
		client: client,
		bucket: cfg.S3Bucket,
	}, nil
}

func (s *s3Store) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        r,
		ContentType: aws.String(contentType),
	}
	_, err := s.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("s3 put %q: %w", key, err)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get %q: %w", key, err)
	}
	return out.Body, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 delete %q: %w", key, err)
	}
	return nil
}
