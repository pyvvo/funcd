# Spec / Prompt de conception — Plateforme funcd, faasd-like, modulaire, single-binary, and inspired by kubernetes internal architecture.


## Context

Design a lightweight serverless platform, inspired by faasd, provisionally named funcd.

The platform must allow deploying, running, exposing, monitoring, and administering serverless functions, or small services on a Linux machine, without mandatory dependency on Kubernetes or Docker.

The plateform will have as well servies to augment the basic serverless capabilities, such as KV storage, blob storage, pub/sub, and other services that can be used by the functions.

- The design must be:
    - modular;
    - single-binary on the platform side;
    - API-first;
    - compatible with SDK/CLI generation via OpenAPI;
    - extensible to multi-node later;
    - compatible with a PaaS vision for wasm / functions / agents;
    - testable in phases with progressive e2e validation;
    - designed to minimize code duplication as much as possible.


## Purpose of the plateform

In a AI era where agents are becoming more and more common, the platform will allow to deploy and run agents as serverless functions, with a focus on:
- simplicity of deployment and operation;
- low resource consumption;
- high performance;
- high observability and monitoring capabilities;
- high security and isolation of the functions;
- high extensibility and modularity of the platform;

The plateform should be build based on the existing tools and libraries to avoid reinventing the wheel, and to leverage the existing ecosystem of serverless functions and agents.

## Components

In order to make this plateform self-contained, we will need to implement the following components:

- **Function** :
    - **Runtime**: A lightweight runtime that can execute serverless functions written in various languages (e.g., Go, Python, Node.js, etc.) and WebAssembly (WASM). This runtime should be able to handle function invocation, scaling, and lifecycle management.
        - We should follow the OpenFunction spec to capture events and invoke the functions. Aws lambda runtime API is also a good source of inspiration.
        - The function should be of kind "serverless", event based (input) and produce response via output or side effects (e.g., storage, pub/sub, etc.).  Stateful functions will rely on the services provided by the platform (e.g., KV storage, blob storage, graph database, etc.) to store and retrieve state.
    - **Containerization**: A containerization system that allows functions to be packaged and deployed
    - **Security and Isolation**: A security and isolation system that ensures that functions are executed in a secure and isolated environment, preventing unauthorized access to the host system and other functions. We will usse existing technologies like kata containers dragonball, firecracker, or cloud hypervisor to provide lightweight isolation for the functions.

- **Services** :
    - **KV Storage**: A key-value storage system that allows functions to store and retrieve data in a fast and efficient manner. We will use existing technologies like etcd, redis, or rocksdb to provide a simple and efficient KV storage for the functions.
    - **Blob Storage**: A blob storage system that allows functions to store and retrieve large binary objects (blobs) in a fast and efficient manner. We will use existing technologies like minio, ceph, or s3 to provide a simple and efficient blob storage for the functions.
    - **Graph database**: A graph database that allows functions to store and query graph data, which can be used for tasks like social network analysis, recommendation, and knowledge graphs. We will use existing technologies like neo4j, arangodb, or dgraph to provide a simple and efficient graph database for the functions. (https://github.com/petgraph/petgraph, https://kuzudb.github.io/ ...)
    - **Crypography services**: A cryptography service that allows functions to perform cryptographic operations, such as encryption, decryption, signing, and verification. https://github.com/tink-crypto/tink-go
    - **Pub/Sub**: A publish/subscribe messaging system that allows functions to communicate with each other in a decoupled manner. We will use existing technologies like nats, kafka, or rabbitmq to provide a simple and efficient pub/sub system for the functions. ( I don't this is still relevant if we have an eventing system ...)
    - **Monitoring and Logging**: A monitoring and logging system that collects metrics, logs, and traces from the functions and the platform itself. We will only be OpenTelemetry compliant, and we will use existing tools like stdout, stderr, and log files, and/or external monitoring systems like victoria-metrics, victoria-logs, victoria-trace, and grafana for visualization and analysis.
    - **Workflow engine**: A workflow engine that allows functions to be composed into complex workflows, with support for conditional branching, parallel execution, and error handling. we could take inspiration from existing workflow engines like temporal, but try to keep it simple and lightweight and rely on message systems like nats.
    - **Vecor database**: A vector database that allows functions to store and retrieve high-dimensional vectors, which can be used for tasks like similarity search, recommendation, and machine learning. We will use existing technologies like pinecone, weaviate, or milvus to provide a simple and efficient vector database for the functions.
    - **Secrets management**: A secrets management system that allows functions to securely store and retrieve sensitive information, such as API keys, database credentials, and other secrets. We will use existing technologies like [openbao](https://openbao.org/)
    - **Eventing system**: An eventing system that allows functions to be triggered by various events, such as HTTP requests, timers, pub/sub messages, or external events from other systems. We will use existing technologies like nats and expiration from aws eventbridge.
        - Argo events example :
        ```mermaid
        flowchart LR
            subgraph External["External Systems"]
                GitHub
                S3["S3/MinIO"]
                MQTT["MQTT/Kafka/etc."]
            end

            subgraph K8s["Kubernetes Cluster / Argo Events Namespace"]
                Webhook["Webhook svc"]
                EventSource["EventSource Pod(s)"]
                EventBus["EventBus (JetStream/Kafka)"]
                Sensor["Sensor Pod(s)"]
                K8sObjs["Create K8s objs<br/>(CRDs, Deployments, ...)"]
                Workflows["Argo Workflows"]
                Calls["HTTP/NATS/Kafka calls"]
            end

            GitHub --> Webhook --> EventSource
            S3 --> EventSource
            MQTT --> EventSource
            EventSource -- publish --> EventBus
            EventBus -- "subscribe / filter / conditions" --> Sensor
            Sensor -- triggers --> K8sObjs
            Sensor -- triggers --> Workflows
            Sensor -- triggers --> Calls
        ```
        - A more generic eventing system example :
        ```mermaid
        flowchart LR
            subgraph External["EXTERNAL PRODUCERS"]
                GitHub
                S3["S3 / SNS / SQS"]
                PubSub["Pub/Sub"]
                MQTT
                Timers
                Other["…"]
            end

            subgraph Sources["EVENT SOURCES"]
                direction TB
                Adapters["adapters: Webhook, Kafka, NATS, Files, …"]
                Normalize["→ normalize to CloudEvents"]
                Publish["→ publish to subject / topic"]
                Adapters --> Normalize --> Publish
            end

            subgraph Bus["EVENT BUS (messaging layer)"]
                direction TB
                subgraph JetStream["NATS JetStream"]
                    Stream["stream: \"default\""]
                    Subjects["subjects: default.&lt;src&gt;.&lt;ev&gt;"]
                end
                subgraph Kafka["Kafka"]
                    TopicsBus["topics per bus"]
                    TopicsSensor["topics per sensor (event / trigger / action)"]
                end
            end

            subgraph Sensors["SENSORS"]
                direction TB
                Subscribe["→ subscribes to subjects / topics"]
                Filter["→ deps logic, filters, transforms, parameters"]
                Resolve["→ resolves expressions e.g. (A || B) && C"]
                Subscribe --> Filter --> Resolve
            end

            subgraph Triggers["TRIGGERS"]
                direction TB
                Execute["→ executes side effects when deps satisfied"]
                Actions["HTTP calls, serverless, msg publish, notify, …"]
                Execute --> Actions
            end

            External --> Sources --> Bus --> Sensors --> Triggers
        ```

        LEGEND (concise):
        - Event Sources: capture external events → convert to CloudEvents → publish to bus (subject/topic).
        - Event Bus: transports events (JetStream subjects or Kafka topics; optional retention/persistence).
        - Sensors: subscribe; filter/transform; evaluate dependency graph; parameterize trigger payloads.
        - Triggers: invoke HTTP APIs, serverless functions, publish messages, send notifications, call workflow engines, etc.

- **External Dependencies** :
    - **Registry**: This could any OCI compliant registry (e.g., Docker Hub, GitHub Container Registry, etc.) and even local registries like [Zot Registry](https://zotregistry.dev/)
    - **Messaging engine**: A messaging engine that allows functions to communicate with each other and with external systems in a decoupled manner. ([Nats/jetstream](https://github.com/nats-io/nats-server))
    - **Monitoring and Logging**: External monitoring and logging systems that can be integrated with the platform to collect metrics, logs, and traces from the functions and the platform itself. ([Victoria-metrics](https://docs.victoriametrics.com/victoriametrics/index.html), [victoria-logs](https://docs.victoriametrics.com/victorialogs/index.html), [victoria-trace](https://docs.victoriametrics.com/victoriatraces/index.html)) + vmauth for authentication and authorization and HTTP proxy of the monitoring systems.... https://docs.victoriametrics.com/victoriametrics/data-ingestion/opentelemetry-collector/ could be used to collect and export metrics, logs, and traces from the platform and the functions to the monitoring systems by bathing them in the OpenTelemetry Collector.
    - **S3-compatible storage**: An S3-compatible storage system that allows functions to store and retrieve large binary objects (blobs) in a fast and efficient manner. That will as well serve as the storage of the metastore of the platform ([slatedb](https://github.com/slatedb/slatedb))


- **Internal components** :

    - **API Server**: An API server that exposes the platform's functionality via a RESTful API, with support for authentication, authorization, and other API management features. 
    - **Controller**: A controller that manages the lifecycle of the functions and the services, including deployment, scaling, and monitoring. The controller will be responsible for ensuring that the desired state of the functions and the services is maintained, and for taking corrective actions when necessary. It will reconcile the desired state of the functions and the services with the actual state.
    - **Scheduler**: A scheduler that schedules the execution of the functions based on various factors, such as resource availability, function priority, and other scheduling policies.
    - **Messaging layer**: A messaging layer that allows the internal components of the platform to communicate with each other in a decoupled manner. We will use existing technologies like nats to provide a simple and efficient messaging layer for the internal components of the platform.
    - **Metastore**: A metastore that stores the metadata of the functions and the services, such as their configuration (CRD like kubernetes), state, and other information. We will use existing technologies like slatedb to provide a simple and efficient metastore for the platform.
    - **Control plane**: The control plane that manages the overall operation of the platform, including the API server, the controller, the scheduler, and the messaging layer. The control plane will be responsible for ensuring that the platform is running smoothly and efficiently, and for taking corrective actions when necessary.
    - **Worker**: The worker that executes the functions and the services, and that provides the necessary resources and environment for their execution.
    - **External providers**: The external providers that provide the necessary resources and services for the execution of the functions and the services, such as the registry, the API gateway, the messaging engine, the monitoring and logging systems, and the S3-compatible storage.
    - **Ingress controller**: An ingress controller that manages the ingress traffic to the functions and the services, and that provides the necessary routing and load balancing capabilities. 
        - **API Gateway**: An API gateway that exposes the functions as HTTP endpoints, MCP, gRPC ... via A[pache APISIX](https://github.com/apache/apisix) or [pinggap](https://github.com/vicanso/pingap), with support for authentication, authorization, rate limiting, and other API management features.



## Architecture

The platform will be designed as a modular, single-binary application that can be deployed on a Linux machine. The architecture will be inspired by the internal architecture of Kubernetes, with a clear separation of concerns between the different components of the platform.

 ### Resources definition (CRD-like)

 #### Concept

 We will define the resources of the platform in a CRD-like manner, similar to Kubernetes or [OAM](https://github.com/oam-dev/spec).

So the control plane could have a contract like this:

- Namespace: A logical grouping of functions and services, similar to Kubernetes namespaces.
- Function: A serverless function that can be deployed and executed on the platform.
- Service: A service that provides additional capabilities to the functions, such as KV storage, blob storage, pub/sub, and other services.
- Event: An event that can trigger the execution of a function, such as an HTTP request, a timer, or a message from a pub/sub system
- ...

Example of a CRD-like definition for a function:

This below CRD is just an example, and it can be extended to include more fields and capabilities as needed.
```yaml
apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: my-function
  namespace: my-namespace

spec:
    runtime: go
    handler: main
    image: my-function:latest
    env:
        - name: MY_ENV_VAR
          value: my-value
  resources:
    limits:
      cpu: "500m"
      memory: "128Mi"
  triggers:
    - name: httpTrigger 
      type: http
      route: /my-function
        method: POST
        auth: none
    - type: timer
      schedule: "*/5 * * * *"
  services:
      - name: my-service
        type: kv
        config:
          bucket: my-bucket
#...
```

This specification can be used to define the desired state of the function, and the controller will be responsible for ensuring that the actual state of the function matches the desired state.

So the Resources definition like function is an high-level Resource definition of :
- Event
- Service
- Config
- Secret

So we could use the same approach for the other resources, and define their desired state in a CRD-like manner, and the controller will be responsible for ensuring that the actual state of the resources matches the desired state.

#### Lifecycle 

| Step | Phase                  | Result                                                                                          |
|------|------------------------|-------------------------------------------------------------------------------------------------|
| 1    | Validate               | YAML/JSON validated, quotas checked                                                             |
| 2    | Store spec             | Raw manifest stored in the metadata store                                                       |
| 3    | Deploy request         | User calls `/deploy`                                                                             |
| 4    | Translate to Vendor RD | The Resource Definition is translated to a vendor-specific format (Apache APISIX, NATS, OpenBAO, etc.) |
| 5    | Slice into tasks       | The OAM is broken down into logical tasks                                                       |
| ...    | There could be more      | The OAM is broken down into logical tasks                                                       |
| n    | Monitor & reconcile    | The controller monitors the function/service and takes corrective actions to maintain desired state |

> Additional steps may occur between 5 and 6 (e.g., scheduling, provisioning).

### Features

#### Namespace

Namespaces are a logical grouping of functions and services, similar to Kubernetes namespaces. Each namespace will have its own set of resources, such as functions, services, events, secrets, and configurations. The namespace will provide isolation between different groups of functions and services, and will allow for multi-tenancy on the platform.

#### Function

Functions are the core building blocks of the platform. Each function will have its own runtime, handler, and configuration, and will be able to interact with other functions and services within the same namespace. Functions will be triggered by events, such as HTTP requests, timers, or messages from a pub/sub system, and will be able to access secrets and configurations as needed.

#### Service

Services provide additional capabilities to the functions, such as KV storage, blob storage, pub/sub, and other services. Each service will have its own configuration and will be able to interact with functions within the same namespace. Services will be managed by the controller, and will be able to scale independently of the functions.

#### Event

Events are the triggers that cause functions to be executed. Events can come from various sources, such as HTTP requests, timers, or messages from a pub/sub system. Each event will have its own configuration and will be able to trigger one or more functions within the same namespace. Events will be managed by the controller, and will be able to scale independently of the functions.

#### Controller

The controller is responsible for managing the lifecycle of the functions and services, including deployment, scaling, and monitoring. The controller will reconcile the desired state of the functions and services with the actual state, and will take corrective actions as needed. The controller will also be responsible for managing events and triggers, and for ensuring that functions are executed in response to events.

#### Config

Configurations provide a way to store configuration information for functions and services, such as environment variables, command-line arguments, and other configuration parameters. Configurations will be managed by the controller, and will be able to be updated dynamically without requiring a redeployment of the functions or services.

#### Secret

Secrets provide a way to store sensitive information for functions and services, such as API keys, database credentials, and other secrets. Secrets will be managed by the controller, and will be able to be updated dynamically without requiring a redeployment of the functions or services.

#### Etc..

### Diagram
