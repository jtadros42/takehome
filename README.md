# Running It Locally

This spins up a local Temporal cluster (Temporal server, Cassandra, Elasticsearch) in
Docker and runs the ranking service against it. The service calls Gemini 2.5 Flash on
Vertex AI, so you'll need a GCP project with Vertex AI enabled.

## Prerequisites

- **Docker Desktop** — give it **at least 6 GB of memory** (Settings → Resources).
  Cassandra and Elasticsearch are memory-hungry on startup; the default 2 GB will fail
  to boot the stack.
- **Go 1.25+**
- **Google Cloud SDK** (`gcloud`) with a project that has the **Vertex AI API** enabled.

## 1. Authenticate to Vertex AI

The service uses Application Default Credentials. Log in and point it at your project:

```bash
gcloud auth application-default login
export GOOGLE_CLOUD_PROJECT=your-gcp-project-id
```

`VERTEX_LOCATION` defaults to `us-central1`; override it if your project uses a
different region.

## 2. Start the Temporal infrastructure

From the repo root:

```bash
docker compose up -d
```

This starts Cassandra, Elasticsearch, the Temporal server, and the Temporal Web UI. The
first boot takes a minute or two while Cassandra initializes and the schema is set up —
the dependent containers wait for health checks, so give it a moment.

- **Temporal Web UI:** http://localhost:8080

## 3. Run the service

From `services/llm`:

```bash
cd services/llm
go run ./app
```

The service registers the Temporal worker and starts an HTTP server on **port 8081**.

## 4. Trigger a ranking job

A seed template (`luxury-cars-us`) is loaded on startup. Kick off a run:

```bash
curl -X POST "http://localhost:8081/workflow/run?template_id=luxury-cars-us"
```

The response contains the `workflow_id`. Open the **Temporal Web UI**
(http://localhost:8080) to watch the workflow execute, inspect its history, and see the
final ranking in the workflow result.

## Teardown

```bash
docker compose down        # stop the cluster
docker compose down -v     # stop and wipe Cassandra/Elasticsearch data
```
