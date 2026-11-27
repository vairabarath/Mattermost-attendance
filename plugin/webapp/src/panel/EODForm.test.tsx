import {render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as api from 'api';
import type {EODForm as Form} from 'api';
import React from 'react';

import EODForm from './EODForm';
import EODSection from './EODSection';

jest.mock('api', () => ({
    ...jest.requireActual('api'),
    fetchEOD: jest.fn(),
    submitEOD: jest.fn(),
}));
const mocked = api as jest.Mocked<typeof api>;
const TEAM = 'tttttttttttttttttttttttttt';

const form = (over: Partial<Form> = {}): Form => ({
    available: true,
    team: {id: TEAM, name: 'Bittrix Technologies'},
    day: '2026-11-27',
    day_label: 'Fri 27 Nov',
    statuses: ['In progress', 'In review', 'Blocked', 'Done'],
    no_change: 'no_change', // the bot's value; shown as "No change"
    tasks: [
        {number: 7, title: 'Domain renewal with the full long title', url: 'https://github.com/o/ops/issues/7', repo: 'o/ops', status: 'In progress', choice: 'no_change'},
        {number: 8, title: 'Pricing page', url: 'https://github.com/o/ops/issues/8', repo: 'o/ops', status: 'Todo', choice: 'Blocked'},
    ],
    done: [{number: 4, title: 'Renew domain', url: 'u4', repo: 'o/ops'}],
    others: [{number: 12, title: 'Hiring post', url: 'u12', repo: 'o/ops', status: 'Todo'}, {number: 13, title: 'Invoice', url: 'u13', repo: 'o/ops', checked: true}],
    did: '',
    blockers: '',
    tomorrow: '',
    min_did: 10,
    max_text: 2000,
    submitted: false,
    state: '{"sig":"x"}',
    ...over,
});

test('shows every part of the report with full titles and pre-selected choices', () => {
    render(
        <EODForm
            teamId={TEAM}
            form={form()}
            onDone={jest.fn()}
            onCancel={jest.fn()}
        />,
    );
    expect(screen.getByText('End-of-day report · Fri 27 Nov')).toBeInTheDocument();
    expect(screen.getByRole('link', {name: 'Domain renewal with the full long title'})).toHaveAttribute('href', 'https://github.com/o/ops/issues/7');
    const g7 = screen.getByRole('radiogroup', {name: /#7/});
    expect(within(g7).getAllByRole('radio')).toHaveLength(5);
    expect(within(g7).getByRole('radio', {name: 'No change'})).toHaveAttribute('aria-checked', 'true');
    expect(screen.queryByText('no_change')).not.toBeInTheDocument();
    expect(within(screen.getByRole('radiogroup', {name: /#8/})).getByRole('radio', {name: 'Blocked'})).toHaveAttribute('aria-checked', 'true');
    expect(screen.getByText(/✅ Renew domain/)).toBeInTheDocument();
    expect(screen.getByRole('checkbox', {name: /Hiring post/})).not.toBeChecked();
    expect(screen.getByRole('checkbox', {name: /Invoice/})).toBeChecked();
    expect(screen.getByText('Send report')).toBeInTheDocument();
});

test('sends choices, other tasks and text; too-short text is caught first', async () => {
    const saved = form({submitted: true, sync_line: 'GitHub: ⏳ syncing…'});
    mocked.submitEOD.mockResolvedValue(saved);
    const onDone = jest.fn();
    render(
        <EODForm
            teamId={TEAM}
            form={form()}
            onDone={onDone}
            onCancel={jest.fn()}
        />,
    );
    await userEvent.click(within(screen.getByRole('radiogroup', {name: /#7/})).getByRole('radio', {name: 'Done'}));
    await userEvent.click(screen.getByRole('checkbox', {name: /Hiring post/}));
    await userEvent.type(screen.getByRole('textbox', {name: /What I did today/}), 'short');
    await userEvent.click(screen.getByText('Send report'));
    expect(screen.getByText('Write at least 10 characters.')).toBeInTheDocument();
    expect(mocked.submitEOD).not.toHaveBeenCalled();

    await userEvent.type(screen.getByRole('textbox', {name: /What I did today/}), ' but now long enough');
    await userEvent.type(screen.getByRole('textbox', {name: /Blockers/}), 'Waiting for review');
    await userEvent.click(screen.getByText('Send report'));
    expect(mocked.submitEOD).toHaveBeenCalledWith(TEAM, '{"sig":"x"}', {
        choices: {s0: 'Done', s1: 'Blocked'},
        others: expect.arrayContaining(['0', '1']),
        did: 'short but now long enough',
        blockers: 'Waiting for review',
        tomorrow: '',
    });
    expect(onDone).toHaveBeenCalledWith(saved);
});

test('field errors from the bot are shown next to the field', async () => {
    mocked.submitEOD.mockRejectedValue(new api.EODSubmitError('Pick tasks from the list.', {others: 'Pick tasks from the list.'}));
    render(
        <EODForm
            teamId={TEAM}
            form={form({did: 'Already long enough text'})}
            onDone={jest.fn()}
            onCancel={jest.fn()}
        />,
    );
    await userEvent.click(screen.getByText('Send report'));
    expect(await screen.findByText('Pick tasks from the list.')).toBeInTheDocument();
});

test('the card: not sent → write; sent → edit; nothing to report', async () => {
    mocked.fetchEOD.mockResolvedValue(form());
    const onOpen = jest.fn();
    const r1 = render(
        <EODSection
            teamId={TEAM}
            open={false}
            onOpen={onOpen}
            onClose={jest.fn()}
        />,
    );
    expect(await screen.findByText('Not sent yet')).toBeInTheDocument();
    await userEvent.click(screen.getByText('Write report'));
    expect(onOpen).toHaveBeenCalled();
    r1.unmount();

    mocked.fetchEOD.mockResolvedValue(form({submitted: true, submitted_at: '2026-11-27T12:40:00Z', sync_line: 'GitHub: ✅ synced'}));
    const r2 = render(
        <EODSection
            teamId={TEAM}
            open={false}
            onOpen={jest.fn()}
            onClose={jest.fn()}
        />,
    );
    expect(await screen.findByText('Sent')).toBeInTheDocument();
    expect(screen.getByText(/GitHub: ✅ synced/)).toBeInTheDocument();
    expect(screen.getByText('Edit report')).toBeInTheDocument();
    r2.unmount();

    mocked.fetchEOD.mockResolvedValue(form({available: false, message: 'You haven\'t worked in Bittrix Technologies today, so there\'s nothing to report yet.'}));
    render(
        <EODSection
            teamId={TEAM}
            open={false}
            onOpen={jest.fn()}
            onClose={jest.fn()}
        />,
    );
    expect(await screen.findByText('Opens when you check out.')).toBeInTheDocument();
    expect(screen.queryByText('Write report')).not.toBeInTheDocument();
});
