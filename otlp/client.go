// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package otlp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	metricsService "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/collector/metrics/v1"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry/doevery"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpcMetadata "google.golang.org/grpc/metadata"
)

const (
	// serviceConfig copied from OTel-Go.
	// https://github.com/open-telemetry/opentelemetry-go/blob/5ed96e92446d2d58d131e0672da613a84c16af7a/exporters/otlp/grpcoptions.go#L37
	serviceConfig = `{
	"methodConfig":[{
		"name":[
			{ "service":"opentelemetry.proto.collector.metrics.v1.MetricsService" },
			{ "service":"opentelemetry.proto.collector.trace.v1.TraceService" }
		],
		"retryPolicy":{
			"MaxAttempts":5,
			"InitialBackoff":"0.3s",
			"MaxBackoff":"5s",
			"BackoffMultiplier":2,
			"RetryableStatusCodes":[
				"UNAVAILABLE",
				"CANCELLED",
				"DEADLINE_EXCEEDED",
				"RESOURCE_EXHAUSTED",
				"ABORTED",
				"OUT_OF_RANGE",
				"UNAVAILABLE",
				"DATA_LOSS"
			]
		}
	}]
}`
)

var (
	exportDuration = telemetry.NewTimer(
		"sidecar.export.duration",
		"duration of the otlp.Export() call",
	)

	connectDuration = telemetry.NewTimer(
		"sidecar.connect.duration",
		"duration of the grpc.Dial() call",
	)
)

// Client allows reading and writing from/to a remote gRPC endpoint. The
// implementation may hit a single backend, so the application should create a
// number of these clients.
type Client struct {
	logger           log.Logger
	url              *url.URL
	timeout          time.Duration
	rootCertificates []string
	headers          grpcMetadata.MD
	compressor       string

	conn *grpc.ClientConn
}

// ClientConfig configures a Client.
type ClientConfig struct {
	Logger           log.Logger
	URL              *url.URL
	Timeout          time.Duration
	RootCertificates []string
	Headers          grpcMetadata.MD
	Compressor       string
}

// NewClient creates a new Client.
func NewClient(conf ClientConfig) *Client {
	logger := conf.Logger
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &Client{
		logger:           logger,
		url:              conf.URL,
		timeout:          conf.Timeout,
		rootCertificates: conf.RootCertificates,
		headers:          conf.Headers,
		compressor:       conf.Compressor,
	}
}

// getConnection will dial a new connection if one is not set.  When
// dialing, this function uses its a new context and the same timeout
// used for Store().
func (c *Client) getConnection(ctx context.Context) (_ *grpc.ClientConn, retErr error) {
	if c.conn != nil {
		return c.conn, nil
	}

	defer connectDuration.Start(ctx).Stop(&retErr)

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	useAuth := c.url.Scheme != "http"
	level.Debug(c.logger).Log(
		"msg", "new OTLP connection",
		"auth", useAuth,
		"url", c.url.String(),
		"timeout", c.timeout)

	dopts := []grpc.DialOption{
		grpc.WithBlock(), // Wait for the connection to be established before using it.
		grpc.WithDefaultServiceConfig(serviceConfig),

		// Note: The Sidecar->OTel gRPC connection is not traced:
		// grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
	}
	if useAuth {
		var tcfg tls.Config
		if len(c.rootCertificates) != 0 {
			certPool := x509.NewCertPool()

			for _, cert := range c.rootCertificates {
				bs, err := ioutil.ReadFile(cert)
				if err != nil {
					return nil, fmt.Errorf("could not read certificate authority certificate: %s: %w", cert, err)
				}

				ok := certPool.AppendCertsFromPEM(bs)
				if !ok {
					return nil, fmt.Errorf("could not parse certificate authority certificate: %s: %w", cert, err)
				}
			}

			tcfg = tls.Config{
				ServerName: c.url.Hostname(),
				RootCAs:    certPool,
			}
		}
		level.Debug(c.logger).Log(
			"msg", "TLS configured",
			"server", c.url.Hostname(),
			"root_certs", fmt.Sprint(c.rootCertificates),
		)
		dopts = append(dopts, grpc.WithTransportCredentials(credentials.NewTLS(&tcfg)))
	} else {
		dopts = append(dopts, grpc.WithInsecure())
	}
	if c.compressor != "" {
		dopts = append(dopts, grpc.WithDefaultCallOptions(grpc.UseCompressor(c.compressor)))
	}
	address := c.url.Hostname()
	if len(c.url.Port()) > 0 {
		address = net.JoinHostPort(address, c.url.Port())
	}
	conn, err := grpc.DialContext(ctx, address, dopts...)
	c.conn = conn
	if err != nil {
		level.Debug(c.logger).Log(
			"msg", "connection status",
			"address", address,
			"err", err,
		)
		return nil, err
	}

	return conn, err
}

// Selftest sends an empty request the endpoint.
func (c *Client) Selftest(ctx context.Context) error {
	// Loop until the context is canceled, allowing for retryable failures.
	for {
		conn, err := c.getConnection(ctx)

		if err == nil {
			service := metricsService.NewMetricsServiceClient(conn)
			empty := &metricsService.ExportMetricsServiceRequest{}

			_, err = service.Export(c.grpcMetadata(ctx), empty)
			if err == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if isRecoverable(err) {
				level.Info(c.logger).Log("msg", "selftest recoverable error, still trying", "err", err)
				continue
			}
		}
		return fmt.Errorf(
			"non-recoverable failure in selftest: %s",
			truncateErrorString(err),
		)
	}
}

// Store sends a batch of samples to the endpoint.
func (c *Client) Store(req *metricsService.ExportMetricsServiceRequest) error {
	tss := req.ResourceMetrics
	if len(tss) == 0 {
		// Nothing to do, return silently.
		return nil
	}

	// Note the call to getConnection() applies its own timeout for Dial().
	ctx := context.Background()
	conn, err := c.getConnection(ctx)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	service := metricsService.NewMetricsServiceClient(conn)

	errors := make(chan error, len(tss)/config.MaxTimeseriesPerRequest+1)
	var wg sync.WaitGroup
	for i := 0; i < len(tss); i += config.MaxTimeseriesPerRequest {
		end := i + config.MaxTimeseriesPerRequest
		if end > len(tss) {
			end = len(tss)
		}
		wg.Add(1)
		go func(begin int, end int) {
			defer wg.Done()
			req_copy := &metricsService.ExportMetricsServiceRequest{
				ResourceMetrics: req.ResourceMetrics[begin:end],
			}

			var err error
			defer exportDuration.Start(ctx).Stop(&err)

			if _, err = service.Export(c.grpcMetadata(ctx), req_copy); err != nil {
				level.Debug(c.logger).Log(
					"msg", "export failure",
					"err", truncateErrorString(err),
				)
				errors <- err
				return
			}

			doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
				level.Debug(c.logger).Log(
					"msg", "successful write",
					"records", end-begin,
				)
			})
		}(i, end)
	}
	wg.Wait()
	close(errors)
	if err, ok := <-errors; ok {
		return err
	}
	return nil
}

func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) grpcMetadata(ctx context.Context) context.Context {
	return grpcMetadata.NewOutgoingContext(ctx, c.headers)
}
