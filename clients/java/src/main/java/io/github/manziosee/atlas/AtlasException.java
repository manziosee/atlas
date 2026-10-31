package io.github.manziosee.atlas;

/**
 * Atlas rejected a request or could not be reached. Branch on {@link #code()}
 * (not_found, not_owner, precondition_failed, ...), not on the message.
 * {@link #status()} is null when the node was unreachable.
 */
public final class AtlasException extends RuntimeException {
  private static final long serialVersionUID = 1L;

  private final Integer status;
  private final String code;
  private final String requestId;
  private final String ownerNode;

  AtlasException(String message, Integer status, String code, String requestId, String ownerNode, Throwable cause) {
    super(message, cause);
    this.status = status;
    this.code = code == null ? "" : code;
    this.requestId = requestId == null ? "" : requestId;
    this.ownerNode = ownerNode == null ? "" : ownerNode;
  }

  /** The HTTP status, or null when the node was unreachable. */
  public Integer status() { return status; }

  /** The stable error code, such as not_found or precondition_failed. */
  public String code() { return code; }

  /** The request ID that appears in the node's access log. */
  public String requestId() { return requestId; }

  /** For not_owner, owner_alive and owner_unreachable: the owning node. */
  public String ownerNode() { return ownerNode; }
}
