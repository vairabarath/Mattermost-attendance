// Renders a bot message (Mattermost markdown) with Mattermost's own
// formatter — the same one used for posts, which escapes HTML — and falls
// back to plain text where it isn't available (tests, old servers).

import React from 'react';

type PostUtils = {
    formatText: (text: string, options?: Record<string, unknown>) => string;
    messageHtmlToComponent: (html: string, options?: Record<string, unknown>) => React.ReactNode;
};

declare global {
    interface Window {
        PostUtils?: PostUtils;
    }
}

// plain strips markdown markers for the fallback.
export function plain(text: string): string {
    return text.
        replace(/\[([^\]]+)\]\([^)]+\)/g, '$1').
        replace(/^#+\s*/gm, '').
        replace(/^>\s?/gm, '').
        replace(/[*`_]/g, '');
}

export default function Markdown({text}: {text: string}) {
    const pu = window.PostUtils;
    if (pu?.formatText && pu?.messageHtmlToComponent) {
        try {
            const html = pu.formatText(text, {markdown: true, atMentions: false, mentionHighlight: false, singleline: false});
            return <div className='att-md'>{pu.messageHtmlToComponent(html, {mentions: false})}</div>;
        } catch {
            // fall through to plain text
        }
    }
    return <div className='att-md att-md--plain'>{plain(text)}</div>;
}
