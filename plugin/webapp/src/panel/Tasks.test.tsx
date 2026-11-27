import {act as reactAct, render, screen, waitFor, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as api from 'api';
import type {TaskItem, TasksState} from 'api';
import React from 'react';

import {REFRESH_EVENT} from './refresh';
import Tasks from './Tasks';

jest.mock('api');
const mocked = api as jest.Mocked<typeof api>;
const TEAM = 'tttttttttttttttttttttttttt';

const task = (n: number, over: Partial<TaskItem> = {}): TaskItem => ({
    number: n,
    title: `Task ${n}`,
    url: `https://github.com/org/app/issues/${n}`,
    repo: 'org/app',
    status: 'Todo',
    overdue: false,
    off_board: false,
    picked: false,
    ...over,
});
const state = (tasks: TaskItem[], over: Partial<TasksState> = {}): TasksState => ({configured: true, linked: true, tasks, ...over});

test('lists tasks with status, overdue, new and picked marks', async () => {
    mocked.fetchTasks.mockResolvedValue(state([
        task(9, {due: '2026-11-25', overdue: true, priority: 'High'}),
        task(15, {status: 'In progress', picked: true}),
        task(30, {off_board: true, title: '<b>not html</b>'}),
    ]));
    render(<Tasks teamId={TEAM}/>);

    const rows = await screen.findAllByRole('listitem');
    expect(rows).toHaveLength(3);
    expect(within(rows[0]).getByRole('link')).toHaveAttribute('href', 'https://github.com/org/app/issues/9');
    expect(within(rows[0]).getByRole('link')).toHaveAttribute('target', '_blank');
    expect(rows[0]).toHaveTextContent('⚠️ overdue');
    expect(rows[0]).toHaveTextContent('High');
    expect(rows[1]).toHaveTextContent('✓ Today');
    expect(within(rows[1]).queryByText('Pick')).not.toBeInTheDocument();
    expect(rows[2]).toHaveTextContent('🆕 added to the board');
    expect(rows[2]).toHaveTextContent('<b>not html</b>'); // shown as text
    expect(mocked.fetchTasks).toHaveBeenCalledWith(TEAM);
});

test('pick a task', async () => {
    mocked.fetchTasks.mockResolvedValue(state([task(15)]));
    mocked.pickTask.mockResolvedValue({message: '▶️ **#15 Task 15** is now **In progress**.', tasks: state([task(15, {status: 'In progress', picked: true})])});
    render(<Tasks teamId={TEAM}/>);

    await userEvent.click(await screen.findByLabelText('Pick #15 Task 15 for today'));
    expect(mocked.pickTask).toHaveBeenCalledWith(TEAM, 'https://github.com/org/app/issues/15');
    expect(await screen.findByRole('status')).toHaveTextContent('#15 Task 15 is now In progress');
    expect(screen.getByText('✓ Today')).toBeInTheDocument();
    expect(screen.getByText('In progress')).toBeInTheDocument();
});

test('show all', async () => {
    mocked.fetchTasks.mockResolvedValue(state([1, 2, 3, 4, 5, 6, 7, 8].map((n) => task(n))));
    render(<Tasks teamId={TEAM}/>);
    expect(await screen.findAllByRole('listitem')).toHaveLength(6);
    await userEvent.click(screen.getByText('Show all 8'));
    expect(screen.getAllByRole('listitem')).toHaveLength(8);
});

test('not linked, GitHub down (retry), nothing to do, not set up', async () => {
    mocked.fetchTasks.mockResolvedValueOnce(state([], {linked: false}));
    const {unmount} = render(<Tasks teamId={TEAM}/>);
    expect(await screen.findByText(/isn’t linked yet/)).toBeInTheDocument();
    unmount();

    mocked.fetchTasks.mockResolvedValueOnce(state([], {error: 'Couldn\'t load your tasks from GitHub right now.'}));
    mocked.fetchTasks.mockResolvedValueOnce(state([]));
    const r2 = render(<Tasks teamId={TEAM}/>);
    expect(await screen.findByText(/Couldn't load your tasks/)).toBeInTheDocument();
    await userEvent.click(screen.getByText('Try again'));
    expect(await screen.findByText(/No open tasks/)).toBeInTheDocument();
    r2.unmount();

    mocked.fetchTasks.mockResolvedValueOnce(state([], {configured: false, linked: false}));
    const {container} = render(<Tasks teamId={TEAM}/>);
    await waitFor(() => expect(container).toBeEmptyDOMElement());
});

test('pick errors are shown; live refresh reloads', async () => {
    mocked.fetchTasks.mockResolvedValue(state([task(15)]));
    mocked.pickTask.mockRejectedValue(new Error('Couldn’t reach the attendance service. Please try again.'));
    render(<Tasks teamId={TEAM}/>);
    await userEvent.click(await screen.findByText('Pick'));
    expect(await screen.findByText(/Couldn’t reach the attendance service/)).toBeInTheDocument();

    mocked.fetchTasks.mockResolvedValue(state([task(15, {picked: true})]));
    reactAct(() => {
        window.dispatchEvent(new Event(REFRESH_EVENT));
    });
    expect(await screen.findByText('✓ Today')).toBeInTheDocument();
});
