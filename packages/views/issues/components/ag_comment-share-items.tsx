// AG patch: "Copy (Slack)" comment menu item. See ag-patches/PATCHES.md.

import { Copy as ClipboardCopy } from "@multica/ui/icons";
import { toast } from "sonner";
import { copyText } from "@multica/ui/lib/clipboard";
import { copyRich } from "@multica/ui/lib/ag_clipboard-rich";
import { useWorkspaceSlug } from "@multica/core/paths";
import { useT } from "../../i18n";
import { useAppOrigin } from "../../navigation/use-app-origin";
import type { RowActionItem } from "../../common/row-actions-menu";
import {
  renderSlackHtml,
  slackPlainText,
  type SlackCopyContext,
} from "./ag_slack-copy";

/**
 * Menu action that copies a comment as Slack-pasteable rich text (`text/html`
 * plus a mrkdwn `text/plain` fallback). Fails soft: if rendering throws it
 * copies the mrkdwn text, and if every clipboard path fails it shows no toast.
 */
export function useCopySlackAction(content: string): RowActionItem {
  const { t } = useT("issues");
  const { t: tAg } = useT("ag");
  const origin = useAppOrigin();
  const workspaceSlug = useWorkspaceSlug();

  const onCopy = async () => {
    const ctx: SlackCopyContext = { origin, workspaceSlug };
    let text: string;
    try {
      text = slackPlainText(content, ctx);
    } catch {
      text = content;
    }
    // The clipboard write starts inside the click with the HTML still pending;
    // awaiting the render first would lose the user gesture in Safari.
    let renderFailed = false;
    const html = renderSlackHtml(content, ctx).catch((err: unknown) => {
      renderFailed = true;
      throw err;
    });
    let ok = await copyRich({ html, text });
    if (!ok && renderFailed) ok = await copyText(text);
    if (ok) toast.success(t(($) => $.comment.copied_toast));
  };

  return {
    key: "copy-slack",
    icon: <ClipboardCopy className="h-3.5 w-3.5" />,
    label: tAg(($) => $.issues.comment.copy_slack_action),
    onSelect: () => void onCopy(),
  };
}
