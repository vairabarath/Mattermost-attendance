// The panel's calls to the bot (through the plugin server). Types mirror
// the bot's JSON (internal/attendance/api.go).

import {doFetch} from 'client';

export type TeamRef = {id: string; name: string};

export type SessionState = {
    team: TeamRef;
    check_in: string;
    mode: 'office' | 'wfh' | string;
    on_break: boolean;
    break_since?: string;
    net_minutes: number;
    net_seconds: number;
};

export type Me = {
    now: string;
    user_id: string;
    team: TeamRef;
    is_admin: boolean;
    github_linked: boolean;
    session: SessionState | null;
    today_team_minutes: number;
    today_all_minutes: number;
    today_team_seconds: number;
    today_all_seconds: number;
};

export type SessionAction = 'in' | 'break' | 'back' | 'out';

export type ActResult = {message: string; me: Me};

const teamQuery = (teamId: string) => `?team=${encodeURIComponent(teamId)}`;

export const fetchMe = (teamId: string) => doFetch<Me>(`/me${teamQuery(teamId)}`);

export const act = (teamId: string, action: SessionAction, text = '') =>
    doFetch<ActResult>(`/session/${action}${teamQuery(teamId)}`, {method: 'post', body: JSON.stringify({text})});

export type TaskItem = {
    number: number;
    title: string;
    url: string;
    repo: string;
    status: string;
    priority?: string;
    due?: string;
    overdue: boolean;
    off_board: boolean;
    picked: boolean;
};

export type TasksState = {
    configured: boolean;
    linked: boolean;
    error?: string;
    tasks: TaskItem[];
};

export const fetchTasks = (teamId: string) => doFetch<TasksState>(`/tasks${teamQuery(teamId)}`);

export const pickTask = (teamId: string, issueURL: string) =>
    doFetch<{message: string; tasks: TasksState}>(`/tasks/pick${teamQuery(teamId)}`, {method: 'post', body: JSON.stringify({issue_url: issueURL})});
