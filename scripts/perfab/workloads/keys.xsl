<!-- xsl:key: two keys over 200,000 records, a key() lookup per record and per item -->
<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:key name="by-id" match="order" use="@id"/>
  <xsl:key name="by-sku" match="item" use="@sku"/>
  <xsl:template match="/">
    <xsl:for-each select="/orders/order">
      <xsl:value-of select="concat(@id, ' ', count(key('by-id', concat('o', position() mod 1000))), ' ', count(key('by-sku', items/item[1]/@sku)), '&#10;')"/>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>
