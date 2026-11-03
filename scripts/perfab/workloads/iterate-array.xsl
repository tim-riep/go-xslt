<!-- XSLT 3.0: xsl:iterate collecting one array member per record with array:append -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="text"/>
  <xsl:template match="/">
    <xsl:iterate select="/orders/order">
      <xsl:param name="ids" as="array(xs:string)" select="[]"/>
      <xsl:on-completion>
        <xsl:value-of select="array:size($ids)"/><xsl:text>&#10;</xsl:text>
        <xsl:value-of select="array:subarray($ids, 1, 100)" separator="&#10;"/>
      </xsl:on-completion>
      <xsl:next-iteration>
        <xsl:with-param name="ids" select="array:append($ids, concat(@id, ':', customer))"/>
      </xsl:next-iteration>
    </xsl:iterate>
  </xsl:template>
</xsl:stylesheet>
