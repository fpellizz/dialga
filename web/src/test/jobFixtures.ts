import type { Job, JobEvent, JobSummary } from "../jobs";

/* Jobs as the engine answers them (internal/jobs). Placeholders only. */

export const JOB_ID = "20261016T090000Z-0000abcd";

/** An ISO time from local clock values, so formatted times do not depend on the zone. */
export function at(hours: number, minutes: number, seconds = 0): string {
  return new Date(2026, 9, 16, hours, minutes, seconds).toISOString();
}

/** An import of two databases: sales uploading, hr queued. */
export function runningJob(): Job {
  return {
    id: JOB_ID,
    kind: "import",
    origin: "app",
    target: "dev-sql",
    instance: "my-sqlserver",
    maxTasks: 2,
    state: "running",
    createdAt: at(11, 0),
    updatedAt: at(11, 1),
    items: [
      {
        name: "sales",
        state: "running",
        step: "upload",
        percent: 0,
        steps: [
          {
            id: "upload",
            title: "Upload sales.bak",
            resource: "transfer",
            status: "running",
            startedAt: at(11, 1),
          },
          { id: "restore", title: "Restore sales", resource: "rds-task", status: "pending" },
        ],
      },
      {
        name: "hr",
        state: "queued",
        percent: 0,
        steps: [
          { id: "upload", title: "Upload hr.bak", resource: "transfer", status: "pending" },
          { id: "restore", title: "Restore hr", resource: "rds-task", status: "pending" },
        ],
      },
    ],
    events: [
      {
        seq: 1,
        time: at(11, 0),
        type: "job.queued",
        message: "import of 2 database(s) on dev-sql",
      },
      { seq: 2, time: at(11, 1), type: "job.started" },
      {
        seq: 3,
        time: at(11, 1),
        type: "step.started",
        item: "sales",
        step: "upload",
        message: "Upload sales.bak",
      },
    ],
  };
}

/** The import above, interrupted: sales in the middle of its upload, hr with a task left running. */
export function interruptedJob(): Job {
  const job = runningJob();
  return {
    ...job,
    state: "interrupted",
    endedAt: at(11, 42),
    items: [
      {
        ...job.items[0],
        state: "interrupted",
        percent: 40,
        steps: [
          { ...job.items[0].steps[0], status: "interrupted", endedAt: at(11, 42) },
          job.items[0].steps[1],
        ],
        upload: {
          bucket: "my-sqlserver-backups",
          key: "import/sales.bak",
          uploadId: "up-9",
          size: 100 * 1024 * 1024,
          partSize: 10 * 1024 * 1024,
          partsDone: [1, 2, 3, 4],
        },
      },
      {
        name: "hr",
        state: "interrupted",
        step: "restore",
        percent: 60,
        steps: [
          { id: "upload", title: "Upload hr.bak", status: "succeeded" },
          { id: "restore", title: "Restore hr", status: "interrupted" },
        ],
        task: { id: 12, type: "RESTORE_DB", lifecycle: "IN_PROGRESS", percent: 60 },
        taskIds: [12],
        taskLeftRunning: true,
      },
    ],
    events: [
      ...job.events,
      {
        seq: 4,
        time: at(11, 42),
        type: "log",
        item: "hr",
        level: "warn",
        message: "Native task 12 (IN_PROGRESS) is still running on the instance",
      },
      {
        seq: 5,
        time: at(11, 42),
        type: "job.interrupted",
        message: "The engine stopped while the job was running.",
      },
    ],
  };
}

export function summary(job: Job, extra: Partial<JobSummary> = {}): JobSummary {
  return {
    id: job.id,
    kind: job.kind,
    operation: job.operation,
    origin: job.origin,
    refusal: job.refusal,
    target: job.target,
    instance: job.instance,
    state: job.state,
    createdAt: job.createdAt,
    updatedAt: job.updatedAt,
    endedAt: job.endedAt,
    cancelRequested: job.cancelRequested,
    databases: job.items.map((i) => i.name),
    succeeded: job.items.filter((i) => i.state === "succeeded").length,
    failed: job.items.filter((i) => i.state === "failed").length,
    ...extra,
  };
}

/** An event of the running job, after its three logged ones. */
export function event(seq: number, type: string, extra: Partial<JobEvent> = {}): JobEvent {
  return { seq, time: at(11, 2, seq), type, ...extra };
}
