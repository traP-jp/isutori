import React from "react";
import ReactDOM from "react-dom";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { ApiClient, ApiError } from "@portal/ApiClient";
import { ContestantApp } from "@portal/ContestantApp";
import { updateNavBarSession } from "@portal/NavbarSession";
import { ErrorSchema } from "@proto/error_pb";
import { BenchmarkJobSchema, BenchmarkJob_Status, type BenchmarkJob } from "@proto/resources/benchmark_job_pb";
import { Contest_Status, ContestSchema } from "@proto/resources/contest_pb";
import { ContestantInstance_Status, ContestantInstanceSchema } from "@proto/resources/contestant_instance_pb";
import { LeaderboardItemSchema, type LeaderboardItem } from "@proto/resources/leaderboard_pb";
import { DashboardResponseSchema } from "@proto/services/audience/dashboard_pb";
import { GetCurrentSessionResponseSchema } from "@proto/services/common/me_pb";
import {
  EnqueueBenchmarkJobResponseSchema,
  GetBenchmarkJobResponseSchema,
  ListBenchmarkJobsResponseSchema,
  type EnqueueBenchmarkJobRequest,
} from "@proto/services/contestant/benchmark_pb";
import { ListClarificationsResponseSchema } from "@proto/services/contestant/clarifications_pb";
import { GetCloudFormationResponseSchema } from "@proto/services/contestant/cloud_formation_pb";
import { ListNotificationsResponseSchema } from "@proto/services/contestant/notifications_pb";

type Outcome = "pass" | "fail" | "error";

interface MockConfig {
  pendingSeconds: number;
  runningSeconds: number;
  outcome: Outcome;
  score: number;
}

interface StoredJob extends MockConfig {
  id: number;
  targetId: number;
  createdAtMs: number;
}

const storageKey = "isutori-mock-portal-jobs";
const team = { id: 1n, name: "isutori" };
const avatarUrl =
  "data:image/svg+xml," +
  encodeURIComponent(
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1" fill="#f5a"/></svg>',
  );
const contestStartMs = Date.now() - 60 * 60 * 1000;
const contest = create(ContestSchema, {
  status: Contest_Status.STARTED,
  registrationOpensAt: timestampFromMs(contestStartMs - 7 * 24 * 60 * 60 * 1000),
  registrationClosesAt: timestampFromMs(contestStartMs - 24 * 60 * 60 * 1000),
  startsAt: timestampFromMs(contestStartMs),
  freezesAt: timestampFromMs(contestStartMs + 7 * 60 * 60 * 1000),
  endsAt: timestampFromMs(contestStartMs + 8 * 60 * 60 * 1000),
});
const instances = [1, 2, 3].map((n) =>
  create(ContestantInstanceSchema, {
    id: BigInt(n),
    teamId: team.id,
    number: BigInt(n),
    cloudId: `i-mock${n}`,
    privateIpv4Address: `192.168.0.1${n}`,
    publicIpv4Address: `203.0.113.1${n}`,
    status: ContestantInstance_Status.RUNNING,
  }),
);

function loadJobs(): StoredJob[] {
  try {
    return JSON.parse(window.localStorage.getItem(storageKey) ?? "[]");
  } catch {
    return [];
  }
}

function saveJobs(jobs: StoredJob[]) {
  window.localStorage.setItem(storageKey, JSON.stringify(jobs));
}

function apiError(code: number, name: string, humanMessage: string, humanDescriptions: string[] = []) {
  return new ApiError(
    new Error(`mock portal returned error ${code}`),
    create(ErrorSchema, { code, name, humanMessage, humanDescriptions }),
  );
}

// Status is derived from elapsed time so that jobs keep progressing across reloads.
function toProto(j: StoredJob, now: number): BenchmarkJob {
  const startedMs = j.createdAtMs + j.pendingSeconds * 1000;
  const finishedMs = startedMs + j.runningSeconds * 1000;
  const job = create(BenchmarkJobSchema, {
    id: BigInt(j.id),
    teamId: team.id,
    targetId: BigInt(j.targetId),
    target: instances.find((i) => i.id === BigInt(j.targetId)),
    instanceName: "mock",
    enqueuedBy: { name: "risuris", avatarUrl },
    createdAt: timestampFromMs(j.createdAtMs),
    updatedAt: timestampFromMs(Math.min(now, finishedMs)),
  });
  if (now < startedMs) {
    job.status = BenchmarkJob_Status.PENDING;
    return job;
  }
  job.startedAt = timestampFromMs(startedMs);
  if (now < finishedMs) {
    job.status = BenchmarkJob_Status.RUNNING;
    job.result = {
      $typeName: "isuxportal.proto.resources.BenchmarkResult",
      finished: false,
      passed: false,
      score: 0n,
      markedAt: timestampFromMs(now),
    };
    return job;
  }
  job.finishedAt = timestampFromMs(finishedMs);
  if (j.outcome === "error") {
    job.status = BenchmarkJob_Status.ERRORED;
    return job;
  }
  const passed = j.outcome === "pass";
  const score = passed ? BigInt(j.score) : 0n;
  job.status = BenchmarkJob_Status.FINISHED;
  job.score = score;
  job.result = {
    $typeName: "isuxportal.proto.resources.BenchmarkResult",
    finished: true,
    passed,
    score,
    scoreBreakdown: {
      $typeName: "isuxportal.proto.resources.BenchmarkResult.ScoreBreakdown",
      raw: score,
      deduction: 0n,
    },
    markedAt: timestampFromMs(finishedMs),
    execution: {
      $typeName: "isuxportal.proto.resources.BenchmarkResult.Execution",
      reason: passed ? "" : "fail: mock portal was configured to fail",
      stdout: [
        'time=00:00:00.000Z level=INFO msg="initialize"',
        'time=00:00:01.000Z level=INFO msg="load"',
        passed
          ? `time=00:01:00.000Z level=INFO msg="score" score=${score}`
          : 'time=00:00:30.000Z level=ERROR msg="validation failed"',
      ].join("\n"),
      stderr: "",
      exitStatus: passed ? 0 : 1,
      exitSignal: 0,
      signaled: false,
    },
  };
  return job;
}

class MockApiClient extends ApiClient {
  private config: MockConfig;

  constructor(config: MockConfig) {
    super("/");
    this.config = config;
  }

  public override async request(path: string): Promise<Response> {
    throw new Error(`mock portal does not implement ${path}`);
  }

  public override async getCurrentSession() {
    return create(GetCurrentSessionResponseSchema, {
      team: { ...team, leaderId: 1n, memberIds: [1n] },
      contestant: { id: 1n, teamId: team.id, name: "risuris", detail: { avatarUrl } },
      contest,
      contestantInstances: instances,
    });
  }

  public override async listBenchmarkJobs(status?: BenchmarkJob_Status, limit?: number) {
    const now = Date.now();
    let jobs = loadJobs()
      .map((j) => toProto(j, now))
      .filter((j) => status === undefined || j.status === status)
      .reverse();
    if (limit) jobs = jobs.slice(0, limit);
    return create(ListBenchmarkJobsResponseSchema, { jobs });
  }

  public override async enqueueBenchmarkJob(payload: EnqueueBenchmarkJobRequest) {
    const now = Date.now();
    const jobs = loadJobs();
    const busy = jobs.some((j) => {
      const s = toProto(j, now).status;
      return s === BenchmarkJob_Status.PENDING || s === BenchmarkJob_Status.RUNNING;
    });
    if (busy) {
      throw apiError(422, "ActiveRecord::RecordInvalid", "Failed to save record", [
        "Team must have no other concurrent job",
      ]);
    }
    if (!instances.some((i) => i.id === payload.targetId)) {
      throw apiError(404, "ActiveRecord::RecordNotFound", "The requested resource was not found");
    }
    const job: StoredJob = {
      ...this.config,
      id: (jobs.at(-1)?.id ?? 0) + 1,
      targetId: Number(payload.targetId),
      createdAtMs: now,
    };
    saveJobs([...jobs, job]);
    return create(EnqueueBenchmarkJobResponseSchema, { job: toProto(job, now) });
  }

  public override async getBenchmarkJob(id: bigint) {
    const job = loadJobs().find((j) => BigInt(j.id) === id);
    if (!job) throw apiError(404, "ActiveRecord::RecordNotFound", "The requested resource was not found");
    return create(GetBenchmarkJobResponseSchema, { job: toProto(job, Date.now()) });
  }

  public override async getContestantMergedDashboard(_id: bigint) {
    const now = Date.now();
    const scores = loadJobs()
      .map((j) => toProto(j, now))
      .filter((j) => j.status === BenchmarkJob_Status.FINISHED)
      .map((j) => ({ score: j.score, startedAt: j.startedAt, markedAt: j.finishedAt }));
    const best = scores.reduce<(typeof scores)[number] | undefined>(
      (a, s) => (a && a.score >= s.score ? a : s),
      undefined,
    );
    const item = create(LeaderboardItemSchema, {
      team,
      latestScore: scores.at(-1),
      bestScore: best,
      scoreHistory: { scores },
    });
    return create(DashboardResponseSchema, {
      leaderboard: { teams: [item], contest, generatedAt: timestampFromMs(now) },
    });
  }

  public override async getAudienceLeaderboardItems(_ids: bigint[]): Promise<LeaderboardItem[]> {
    return [];
  }

  public override async listNotifications(_after?: bigint) {
    return create(ListNotificationsResponseSchema, {});
  }

  public override async listClarifications() {
    return create(ListClarificationsResponseSchema, {});
  }

  public override async getCloudFormation() {
    return create(GetCloudFormationResponseSchema, {});
  }
}

function readConfig(): MockConfig {
  const meta = document.querySelector<HTMLMetaElement>('meta[name="isutori:mock"]');
  if (!meta) throw new Error('meta[name="isutori:mock"] is missing');
  return JSON.parse(meta.content);
}

(async function () {
  const client = new MockApiClient(readConfig());
  const session = await client.getCurrentSession();
  updateNavBarSession(session);
  ReactDOM.render(<ContestantApp session={session} client={client} release="mock" />, document.getElementById("app"));
})();
