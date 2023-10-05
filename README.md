# Notifi-Scaler

## **Overview**

The **`elastic-table-kube`** project is a Kubernetes-based system designed to scale works horizontally among workers based on the number of values in a table. The project includes Kubernetes manifests to deploy a control plane, workers, and database components. The workers process subsets of the table, incrementing a **`Value`** column and updating the **`CurrentWorker`** column as they go along.

The primary objective is to balance the workload across multiple worker nodes and to adapt to changes in the table or the number of workers dynamically. To achieve that, a consistent hashing algorithm in implemented.

## System Requirement

Given a table of values with the following schema, our system horizontally scale up and down workers based on the number of values in the table. When a worker processing a subset of the table is removed, the workload will be evenly distributed across the remaining workers until a new worker node is added.

Table Schema:

- ID: uuid
- Value: integer
- CurrentWorker: string

While a worker is processing a subset, it will increment the Value by 1 every second and set the CurrentWorker value to the name of the worker pod.

Every second, the worker will print the list of IDs being watched in the table. This list will remain stable if there are no changes to the number of values in the table or the number of available workers. However, if there is a change to either, a redistribution will be performed to minimize the amount of change across workers using consistent hashing algorithm.

## Structure

```
➜  notifi-scaler git:(main) ✗ tree
.
├── Chart.yaml
├── README.md
├── images
│   ├── control-plane
│   │   ├── Dockerfile
│   │   └── main.go
│   └── worker
│       ├── Dockerfile
│       └── main.go
├── static
│   └── architecture.png
├── templates
│   ├── control-plane-deployment.yaml
│   ├── control-plane-service.yaml
│   ├── db-configmap.yaml
│   ├── db-pv.yaml
│   ├── db-pvc.yaml
│   ├── db-secret.yaml
│   ├── db-service.yaml
│   ├── db-statefulset.yaml
│   ├── db-storageclass.yaml
│   ├── redis-deployment.yaml
│   ├── redis-service.yaml
│   ├── worker-deployment.yaml
│   └── worker-service.yaml
└── values.yaml
```

## **Requirements**

- Helm v3.x
- Kubernetes v1.27.x

## **Architecture**

![Architecture Diagram](static/architecture.png)

## ****Quick Start****

- Deploy

```bash
git clone https://github.com/yourusername/notifi-scaler.git
cd notifi-scaler
helm install notifi-scaler .
```

- Monitor

```bash

# k8s
while true; do clear; kubectl get pods; sleep 1; done

# control-plane
while true; do kubectl logs $(kubectl get pods -l app=control-plane -o jsonpath='{.items[0].metadata.name}'); sleep 1; done

# all workers
for hash in $(kubectl get pods -l app=worker -o=jsonpath='{.items[*].metadata.labels.pod-template-hash}'); do kubectl logs -l app=worker,pod-template-hash=$hash --all-containers=true; done

# single worker
kubectl logs <worker-pod-name>

# database
while true; do clear; kubectl exec -it postgres-0 -- psql -U user -d mydatabase -c "SELECT * FROM public.work_items ORDER BY currentWorker;"; sleep 1; done

# redis
POD_NAME=$(kubectl get pods -l app=redis -o=jsonpath='{.items[0].metadata.name}' -n default) && while true; do clear; kubectl exec -it "$POD_NAME" -- redis-cli hgetall workers; sleep 1; done
```

- Scale

```bash
helm upgrade notifi-scaler-release . --set worker.replicaCount=<number-of-replicas>
```

## **Areas of Improvement**

- To manage concurrency, I have implemented atomic operations in Redis to mitigate risks. However, this is just the initial layer of defense against issues like race conditions and deadlocks. To ensure robust protection for the system, we need to integrate advanced synchronization techniques. This could involve using locks at the Go or process level, or utilizing database-level transactions.
- During testing, both the control plane and worker nodes demonstrated their ability to handle sudden spikes in workload. However, if we want to be fully prepared for extreme variations in load, we should implement rate-limiting as well as back-off and retry strategies.
- For resilience, we must incorporate failover mechanisms for our key components (control plane, Redis, and Postgres). This may involve setting up the control plane in a clustered manner and enabling data replication for the databases, making our architecture much more resistant to individual component failures.
- Currently, we have basic monitoring and logging in place, but we could greatly benefit from a more comprehensive setup that provides real-time insights into system performance and health.
- On the security front, it is crucial to secure communication channels with SSL/TLS encryption and enforce Role-Based Access Control (RBAC) for system interactions. This will enhance system integrity.
