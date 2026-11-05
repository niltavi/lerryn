import { Modal } from "@heroui/react";
import type { ReactNode } from "react";

export interface ConsoleDialogProps {
  isOpen: boolean;
  onOpenChange: (open: boolean) => void;
  /** Heading content; an icon may precede the text. */
  title: ReactNode;
  children?: ReactNode;
  footer: ReactNode;
  /** Whether a click outside or Escape closes it. Off for dialogs that show a secret once. */
  isDismissable?: boolean;
  /** lg for wide content such as a diff. */
  size?: "sm" | "lg";
}

/** A modal dialog in the A-flute style: heading, body, and right-aligned actions. */
export function ConsoleDialog({ isOpen, onOpenChange, title, children, footer, isDismissable = true, size = "sm" }: ConsoleDialogProps) {
  return (
    <Modal.Backdrop
      isOpen={isOpen}
      onOpenChange={onOpenChange}
      isDismissable={isDismissable}
      isKeyboardDismissDisabled={!isDismissable}
    >
      <Modal.Container placement="center" size={size}>
        <Modal.Dialog>
          <Modal.Header>
            <Modal.Heading className="row" style={{ gap: "var(--space-3)" }}>
              {title}
            </Modal.Heading>
          </Modal.Header>
          {children ? <Modal.Body className="stack" style={{ gap: "var(--space-5)" }}>{children}</Modal.Body> : null}
          <Modal.Footer className="row" style={{ justifyContent: "flex-end" }}>
            {footer}
          </Modal.Footer>
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  );
}
