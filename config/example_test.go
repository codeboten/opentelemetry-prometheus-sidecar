package config

import (
        "encoding/json"
        "fmt"
        "io/ioutil"
        "log"
)

func Example() {
        cfg, _, _, err := Configure([]string{
                "program",
                "--config-file=./sidecar.example.yaml",
        }, ioutil.ReadFile)
        if err != nil {
                log.Fatal(err)
        }

        data, err := json.MarshalIndent(cfg, "", "  ")
        if err != nil {
                log.Fatal(err)
        }

        fmt.Println(string(data))

        // Output:
        // {
        //   "destination": {
        //     "endpoint": "https://otlp.io:443",
        //     "headers": {
        //       "access-token": "aabbccdd...wwxxyyzz"
        //     },
        //     "attributes": {
        //       "environment": "public",
        //       "service.name": "demo"
        //     },
        //     "timeout": "2m0s",
        //     "compression": "snappy"
        //   },
        //   "prometheus": {
        //     "endpoint": "http://127.0.0.1:19090",
        //     "wal": "/volume/wal",
        //     "max_point_age": "72h0m0s"
        //   },
        //   "opentelemetry": {
        //     "metrics_prefix": "prefix.",
        //     "use_meta_labels": true
        //   },
        //   "admin": {
        //     "listen_ip": "0.0.0.0",
        //     "port": 10000
        //   },
        //   "security": {
        //     "root_certificates": [
        //       "/certs/root1.crt",
        //       "/certs/root2.crt"
        //     ]
        //   },
        //   "diagnostics": {
        //     "endpoint": "https://otlp.io:443",
        //     "headers": {
        //       "access-token": "wwxxyyzz...aabbccdd"
        //     },
        //     "attributes": {
        //       "environment": "internal"
        //     },
        //     "timeout": "1m0s",
        //     "compression": "snappy"
        //   },
        //   "startup_delay": "30s",
        //   "startup_timeout": "5m0s",
        //   "filters": [
        //     "metric{label=value}",
        //     "other{l1=v1,l2=v2}"
        //   ],
        //   "metric_renames": [
        //     {
        //       "from": "old_metric",
        //       "to": "new_metric"
        //     },
        //     {
        //       "from": "mistake",
        //       "to": "correct"
        //     }
        //   ],
        //   "static_metadata": [
        //     {
        //       "metric": "network_bps",
        //       "type": "counter",
        //       "value_type": "int64",
        //       "help": "Number of bits transferred by this process."
        //     }
        //   ],
        //   "log_config": {
        //     "level": "debug",
        //     "format": "json",
        //     "verbose": 1
        //   },
        //   "disable_supervisor": false,
        //   "disable_diagnostics": false
        // }
}
