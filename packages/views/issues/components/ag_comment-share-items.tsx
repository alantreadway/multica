// AG patch: "Copy (Slack)" comment menu item. See ag-patches/PATCHES.md.

import { useEffect } from "react";
import { ClipboardCopy } from "lucide-react";
import { toast } from "sonner";
import { DropdownMenuItem } from "@multica/ui/components/ui/dropdown-menu";
import { copyText } from "@multica/ui/lib/clipboard";
import { copyRich } from "@multica/ui/lib/ag_clipboard-rich";
import { useWorkspaceSlug } from "@multica/core/paths";
import { useT } from "../../i18n";
import { useAppOrigin } from "../../navigation/use-app-origin";
import {
  preloadSlackRenderer,
  renderSlackHtml,
  slackPlainText,
  type SlackCopyContext,
} from "./ag_slack-copy";

/**
 * Menu item that copies a comment as Slack-pasteable rich text (`text/html`
 * plus a mrkdwn `text/plain` fallback). Fails soft: if rendering throws it
 * copies the mrkdwn text, and if every clipboard path fails it shows no toast.
 */
export function CopySlackMenuItem({ content }: { content: string }) {
  const { t } = useT("issues");
  const origin = useAppOrigin();
  const workspaceSlug = useWorkspaceSlug();

  // Mounted when the menu opens: load the renderer before the click.
  useEffect(() => {
    preloadSlackRenderer();
  }, []);

  const onCopy = async () => {
    const ctx: SlackCopyContext = { origin, workspaceSlug };
    let text: string;
    let ok: boolean;
    try {
      text = slackPlainText(content, ctx);
    } catch {
      text = content;
    }
    try {
      const html = await renderSlackHtml(content, ctx);
      ok = await copyRich({ html, text });
    } catch {
      ok = await copyText(text);
    }
    if (ok) toast.success(t(($) => $.comment.copied_toast));
  };

  return (
    <DropdownMenuItem onClick={() => void onCopy()}>
      <ClipboardCopy className="h-3.5 w-3.5" />
      {t(($) => $.comment.copy_slack_action)}
    </DropdownMenuItem>
  );
}
